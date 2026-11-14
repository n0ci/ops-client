// Package keychain stores the bootstrap bundle: the macOS login keychain via
// the security CLI, or a 0600 file under $XDG_CONFIG_HOME on other systems.
// The OPS_BUNDLE environment variable always takes precedence on read.
package keychain

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	Service = "ops-bootstrap"
	Account = "bundle"
	EnvVar  = "OPS_BUNDLE"
)

// ErrNotFound means no bundle is stored.
var ErrNotFound = errors.New("no bundle stored (run `ops login`)")

// Load returns the stored bundle string.
func Load() (string, error) {
	if v := strings.TrimSpace(os.Getenv(EnvVar)); v != "" {
		return v, nil
	}
	if runtime.GOOS == "darwin" {
		var out, errb bytes.Buffer
		cmd := exec.Command("security", "find-generic-password", "-s", Service, "-a", Account, "-w")
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			if strings.Contains(errb.String(), "could not be found") {
				return "", ErrNotFound
			}
			return "", fmt.Errorf("keychain: %s", strings.TrimSpace(errb.String()))
		}
		return strings.TrimSpace(out.String()), nil
	}
	return LoadFile(FilePath())
}

// Save stores the bundle string and returns a human description of where.
func Save(v string) (string, error) {
	if runtime.GOOS == "darwin" {
		var errb bytes.Buffer
		cmd := exec.Command("security", "add-generic-password", "-U", "-s", Service, "-a", Account,
			"-l", "ops bootstrap bundle", "-w", v)
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("keychain: %s", strings.TrimSpace(errb.String()))
		}
		return "macOS login keychain (service " + Service + ")", nil
	}
	p := FilePath()
	return p, SaveFile(p, v)
}

// FilePath is the non-macOS bundle location.
func FilePath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		h, _ := os.UserHomeDir()
		dir = filepath.Join(h, ".config")
	}
	return filepath.Join(dir, "ops", "bundle")
}

// LoadFile reads a bundle file, refusing group/world-readable files.
func LoadFile(p string) (string, error) {
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s has mode %v; run: chmod 600 %s", p, fi.Mode().Perm(), p)
	}
	b, err := os.ReadFile(p)
	return strings.TrimSpace(string(b)), err
}

// SaveFile atomically writes a 0600 bundle file.
func SaveFile(p, v string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".bundle-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(v + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}
