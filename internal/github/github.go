// Package github is a minimal GitHub REST client that authenticates as
// GitHub App installations. Installation tokens are cached in memory only.
package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/n0ci/ops-client/internal/bundle"
)

// DefaultBase is the GitHub API root; override with OPS_GITHUB_API.
const DefaultBase = "https://api.github.com"

// UserAgent is sent with every request.
var UserAgent = "ops-client"

// JWT builds an RS256 GitHub App JWT (iat = now-60s, exp = now+540s).
func JWT(appID int64, key *rsa.PrivateKey, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	hdr := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(540 * time.Second).Unix(),
		"iss": strconv.FormatInt(appID, 10),
	})
	signing := hdr + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// Token is an installation access token.
type Token struct {
	Value   string    `json:"token"`
	Expires time.Time `json:"expires_at"`
}

// APIError is a non-2xx GitHub response.
type APIError struct {
	Method, Path string
	Status       int
	Message      string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub %s %s: %d %s", e.Method, e.Path, e.Status, e.Message)
}

// IsStatus reports whether err is an APIError with the given status.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// Client talks to GitHub as the bundle's apps.
type Client struct {
	Base  string
	HTTP  *http.Client
	Owner string
	Now   func() time.Time

	apps  map[string]*bundle.App
	mu    sync.Mutex
	cache map[string]Token
}

// New returns a client for the bundle's apps.
func New(b *bundle.Bundle) *Client {
	base := strings.TrimRight(os.Getenv("OPS_GITHUB_API"), "/")
	if base == "" {
		base = DefaultBase
	}
	return &Client{
		Base:  base,
		HTTP:  &http.Client{Timeout: 60 * time.Second},
		Owner: b.Owner,
		Now:   time.Now,
		apps:  b.Apps,
		cache: map[string]Token{},
	}
}

// Token returns an installation token for app ("source" or "dispatch"),
// minting a new one when the cached token is missing or near expiry.
func (c *Client) Token(ctx context.Context, app string) (Token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.cache[app]; ok && t.Expires.After(c.Now().Add(5*time.Minute)) {
		return t, nil
	}
	a := c.apps[app]
	if a == nil || a.Key() == nil {
		return Token{}, fmt.Errorf("unknown app %q", app)
	}
	jwt, err := JWT(a.AppID, a.Key(), c.Now())
	if err != nil {
		return Token{}, err
	}
	var t Token
	path := fmt.Sprintf("/app/installations/%d/access_tokens", a.InstallationID)
	if err := c.do(ctx, "Bearer "+jwt, http.MethodPost, path, nil, &t); err != nil {
		return Token{}, fmt.Errorf("mint %s token: %w", app, err)
	}
	if t.Value == "" {
		return Token{}, fmt.Errorf("mint %s token: empty token in response", app)
	}
	c.cache[app] = t
	return t, nil
}

// Do performs an authenticated JSON request as app. body and out may be nil.
func (c *Client) Do(ctx context.Context, app, method, path string, body, out any) error {
	t, err := c.Token(ctx, app)
	if err != nil {
		return err
	}
	return c.do(ctx, "token "+t.Value, method, path, body, out)
}

func (c *Client) do(ctx context.Context, auth, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return apiError(method, path, resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if p, ok := out.(*[]byte); ok {
		*p, err = io.ReadAll(resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func apiError(method, path string, resp *http.Response) error {
	var m struct {
		Message string `json:"message"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(b, &m) != nil || m.Message == "" {
		m.Message = http.StatusText(resp.StatusCode)
	}
	return &APIError{Method: method, Path: path, Status: resp.StatusCode, Message: m.Message}
}

// Raw fetches a file's raw contents via the contents API.
func (c *Client) Raw(ctx context.Context, app, repo, file string) ([]byte, error) {
	t, err := c.Token(ctx, app)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/contents/%s", c.Owner, repo, file)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+t.Value)
	req.Header.Set("Accept", "application/vnd.github.raw+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError("GET", path, resp)
	}
	return io.ReadAll(resp.Body)
}

// Download GETs path as app without following redirects, then fetches the
// redirect target without the Authorization header (it is a pre-signed URL).
func (c *Client) Download(ctx context.Context, app, path string) ([]byte, error) {
	t, err := c.Token(ctx, app)
	if err != nil {
		return nil, err
	}
	noFollow := *c.HTTP
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+t.Value)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := noFollow.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return io.ReadAll(resp.Body)
	case resp.StatusCode/100 == 3:
		loc, err := resp.Location()
		if err != nil {
			return nil, fmt.Errorf("GET %s: redirect without location", path)
		}
		r2, err := http.NewRequestWithContext(ctx, http.MethodGet, loc.String(), nil)
		if err != nil {
			return nil, err
		}
		r2.Header.Set("User-Agent", UserAgent)
		resp2, err := c.HTTP.Do(r2)
		if err != nil {
			return nil, err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s (redirected): %s", path, resp2.Status)
		}
		return io.ReadAll(resp2.Body)
	default:
		return nil, apiError("GET", path, resp)
	}
}

// Run is a workflow run.
type Run struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	DisplayTitle string    `json:"display_title"`
	Path         string    `json:"path"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	HTMLURL      string    `json:"html_url"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Job is a workflow job.
type Job struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// RunList is the workflow-runs list response.
type RunList struct {
	Runs []Run `json:"workflow_runs"`
}

// JobList is the run-jobs list response.
type JobList struct {
	Jobs []Job `json:"jobs"`
}
