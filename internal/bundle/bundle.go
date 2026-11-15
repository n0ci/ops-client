// Package bundle decodes and validates the ops bootstrap bundle
// ("ops1:" + base64(JSON)) described in platform-control/docs/CONTRACT.md.
package bundle

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Prefix marks a version-1 bundle string.
const Prefix = "ops1:"

// App is one GitHub App identity from the bundle.
type App struct {
	AppID          int64  `json:"app_id"`
	InstallationID int64  `json:"installation_id"`
	Slug           string `json:"slug"`
	PrivateKey     string `json:"private_key"`

	key *rsa.PrivateKey
}

// Key returns the parsed RSA private key (available after Decode).
func (a *App) Key() *rsa.PrivateKey { return a.key }

// Bundle is the decoded bootstrap bundle.
type Bundle struct {
	Version int             `json:"version"`
	Owner   string          `json:"owner"`
	Workdir string          `json:"workdir"` // optional; see ResolveWorkdir
	Apps    map[string]*App `json:"apps"`
}

// Source and Dispatch return the two required app identities.
func (b *Bundle) Source() *App   { return b.Apps["source"] }
func (b *Bundle) Dispatch() *App { return b.Apps["dispatch"] }

var ownerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// Decode parses and validates a bundle string. Whitespace (including line
// breaks introduced by note apps) is ignored; standard and URL-safe base64,
// padded or not, are accepted. Errors never include key material.
func Decode(s string) (*Bundle, error) {
	s = strings.Join(strings.Fields(s), "")
	if !strings.HasPrefix(s, Prefix) {
		return nil, errors.New("bundle: missing \"ops1:\" prefix (copy the whole note)")
	}
	raw, err := decodeBase64(strings.TrimPrefix(s, Prefix))
	if err != nil {
		return nil, errors.New("bundle: payload is not valid base64")
	}
	var b Bundle
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(&b); err != nil {
		return nil, errors.New("bundle: payload is not valid JSON")
	}
	if b.Version != 1 {
		return nil, fmt.Errorf("bundle: unsupported version %d (want 1)", b.Version)
	}
	if b.Owner == "" {
		b.Owner = "n0ci"
	}
	if !ownerRe.MatchString(b.Owner) {
		return nil, errors.New("bundle: invalid owner")
	}
	for _, name := range []string{"source", "dispatch"} {
		a := b.Apps[name]
		if a == nil {
			return nil, fmt.Errorf("bundle: missing app %q", name)
		}
		if a.AppID <= 0 || a.InstallationID <= 0 {
			return nil, fmt.Errorf("bundle: app %q needs app_id and installation_id", name)
		}
		k, err := ParseKey(a.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("bundle: app %q: %w", name, err)
		}
		a.key = k
	}
	return &b, nil
}

func decodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := enc.DecodeString(s); err == nil {
			return raw, nil
		}
	}
	return nil, errors.New("invalid base64")
}

// Encode produces a bundle string from b (used by tests and tooling).
func Encode(b *Bundle) (string, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return Prefix + base64.StdEncoding.EncodeToString(raw), nil
}

// ParseKey parses a PEM RSA private key in PKCS#1 or PKCS#8 form.
func ParseKey(pemText string) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(pemText))
	if blk == nil {
		return nil, errors.New("private_key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, errors.New("private_key is not a PKCS#1 or PKCS#8 RSA key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private_key is not an RSA key")
	}
	return rk, nil
}

// DefaultWorkdir is where the ops repositories live unless overridden. It is a
// folder of its own so that agents started elsewhere in ~/repo do not pick up
// the ops workspace guide and MCP registration.
const DefaultWorkdir = "~/repo/ops"

// WorkdirEnv overrides the workdir (and the bundle's "workdir").
const WorkdirEnv = "OPS_WORKDIR"

// legacyWorkdir is the default before 0.3.2. Existing bundles carry it
// explicitly, so it counts as "not configured" rather than as a choice.
const legacyWorkdir = "~/repo"

// ConfiguredWorkdir is the bundle's "workdir" unless empty or the legacy default.
func ConfiguredWorkdir(configured string) string {
	v := strings.TrimSpace(configured)
	if strings.TrimRight(v, "/") == legacyWorkdir {
		return ""
	}
	return v
}

// ResolveWorkdir picks the workdir, unexpanded: env (the OPS_WORKDIR value)
// first, then the bundle's configured "workdir" (the legacy "~/repo" is
// ignored), then DefaultWorkdir.
func ResolveWorkdir(env, configured string) string {
	if v := strings.TrimSpace(env); v != "" {
		return v
	}
	if v := ConfiguredWorkdir(configured); v != "" {
		return v
	}
	return DefaultWorkdir
}

// Workdir is the workdir for b (which may be nil), unexpanded.
func Workdir(b *Bundle) string {
	configured := ""
	if b != nil {
		configured = b.Workdir
	}
	return ResolveWorkdir(os.Getenv(WorkdirEnv), configured)
}

// WorkdirPath returns the resolved workdir with a leading "~" expanded.
func (b *Bundle) WorkdirPath() string { return ExpandHome(Workdir(b)) }

// ExpandHome expands a leading "~" or "~/".
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
