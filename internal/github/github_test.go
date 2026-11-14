package github_test

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n0ci/ops-client/internal/github"
	"github.com/n0ci/ops-client/internal/testutil"
)

// verifyJWT checks an RS256 JWT against pub and returns its claims.
func verifyJWT(t *testing.T, tok string, pub *rsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	dec := base64.RawURLEncoding
	var hdr, claims map[string]any
	for i, dst := range []*map[string]any{&hdr, &claims} {
		raw, err := dec.DecodeString(parts[i])
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
	}
	if hdr["alg"] != "RS256" || hdr["typ"] != "JWT" {
		t.Fatalf("header = %v", hdr)
	}
	sig, err := dec.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature: %v", err)
	}
	return claims
}

func TestJWT(t *testing.T) {
	k := testutil.Key(t)
	now := time.Unix(1_800_000_000, 0)
	tok, err := github.JWT(123, k, now)
	if err != nil {
		t.Fatal(err)
	}
	c := verifyJWT(t, tok, &k.PublicKey)
	if c["iss"] != "123" || c["iat"] != float64(now.Unix()-60) || c["exp"] != float64(now.Unix()+540) {
		t.Fatalf("claims = %v", c)
	}
	tampered := tok[:len(tok)-4] + "AAAA"
	parts := strings.Split(tampered, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, sum[:], sig) == nil {
		t.Fatal("tampered signature verified")
	}
}

func TestTokenMintAndCache(t *testing.T) {
	k := testutil.Key(t)
	var mints atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/app/installations/") {
			http.NotFound(w, r)
			return
		}
		c := verifyJWT(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &k.PublicKey)
		mints.Add(1)
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"token":"tok-%s-%s","expires_at":%q}`, c["iss"], strings.Split(r.URL.Path, "/")[3],
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer srv.Close()
	t.Setenv("OPS_GITHUB_API", srv.URL+"/")
	c := github.New(testutil.Decoded(t))
	if c.Base != srv.URL {
		t.Fatalf("base = %q", c.Base)
	}
	ctx := context.Background()
	for range 3 {
		tok, err := c.Token(ctx, "source")
		if err != nil {
			t.Fatal(err)
		}
		if tok.Value != "tok-123-456" {
			t.Fatalf("token = %q", tok.Value)
		}
	}
	if tok, _ := c.Token(ctx, "dispatch"); tok.Value != "tok-124-457" {
		t.Fatalf("dispatch token = %q", tok.Value)
	}
	if n := mints.Load(); n != 2 {
		t.Fatalf("minted %d times, want 2 (cached per app)", n)
	}
	if _, err := c.Token(ctx, "nope"); err == nil {
		t.Fatal("unknown app should fail")
	}
}

func TestAPIErrorAndDownloadRedirect(t *testing.T) {
	var blobAuth atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"token":"t","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("GET /missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	})
	var srvURL string
	mux.HandleFunc("GET /logs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srvURL+"/blob?sig=x", http.StatusFound)
	})
	mux.HandleFunc("GET /blob", func(w http.ResponseWriter, r *http.Request) {
		blobAuth.Store(r.Header.Get("Authorization"))
		fmt.Fprint(w, "log text")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL
	t.Setenv("OPS_GITHUB_API", srv.URL)
	c := github.New(testutil.Decoded(t))
	err := c.Do(context.Background(), "source", "GET", "/missing", nil, nil)
	if !github.IsStatus(err, 404) || !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("err = %v", err)
	}
	b, err := c.Download(context.Background(), "dispatch", "/logs")
	if err != nil || string(b) != "log text" {
		t.Fatalf("download = %q, %v", b, err)
	}
	if a := blobAuth.Load(); a != "" {
		t.Fatalf("redirect target received Authorization %q", a)
	}
}
