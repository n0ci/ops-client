package bundle_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/n0ci/ops-client/internal/bundle"
	"github.com/n0ci/ops-client/internal/testutil"
)

func TestDecodeValid(t *testing.T) {
	s := testutil.Encoded(t)
	// Line-wrapped as a notes app might store it.
	wrapped := s[:30] + "\n  " + s[30:200] + "\r\n" + s[200:] + "\n"
	for name, in := range map[string]string{"plain": s, "wrapped": wrapped} {
		b, err := bundle.Decode(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b.Owner != "n0ci" || b.Source().AppID != 123 || b.Dispatch().InstallationID != 457 {
			t.Fatalf("%s: unexpected bundle %+v", name, b)
		}
		if b.Source().Key() == nil || b.Dispatch().Key() == nil {
			t.Fatalf("%s: keys not parsed (PKCS#1 source, PKCS#8 dispatch)", name)
		}
	}
}

func TestDecodeURLSafeUnpadded(t *testing.T) {
	raw, _ := json.Marshal(testutil.Bundle(t))
	s := bundle.Prefix + base64.RawURLEncoding.EncodeToString(raw)
	if _, err := bundle.Decode(s); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeDefaults(t *testing.T) {
	b := testutil.Bundle(t)
	b.Owner, b.Workdir = "", ""
	s, _ := bundle.Encode(b)
	got, err := bundle.Decode(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "n0ci" || got.Workdir != "~/repo" {
		t.Fatalf("defaults not applied: %q %q", got.Owner, got.Workdir)
	}
	if strings.HasPrefix(got.WorkdirPath(), "~") {
		t.Fatalf("workdir not expanded: %s", got.WorkdirPath())
	}
}

func TestDecodeInvalid(t *testing.T) {
	enc := func(mut func(b *bundle.Bundle)) string {
		b := testutil.Bundle(t)
		mut(b)
		s, _ := bundle.Encode(b)
		return s
	}
	cases := map[string]string{
		"no prefix":     strings.TrimPrefix(testutil.Encoded(t), bundle.Prefix),
		"bad base64":    "ops1:!!!not-base64!!!",
		"bad json":      "ops1:" + base64.StdEncoding.EncodeToString([]byte("{nope")),
		"version":       enc(func(b *bundle.Bundle) { b.Version = 2 }),
		"missing app":   enc(func(b *bundle.Bundle) { delete(b.Apps, "dispatch") }),
		"no app id":     enc(func(b *bundle.Bundle) { b.Apps["source"].AppID = 0 }),
		"no install id": enc(func(b *bundle.Bundle) { b.Apps["dispatch"].InstallationID = 0 }),
		"bad owner":     enc(func(b *bundle.Bundle) { b.Owner = "a/b" }),
		"key not pem":   enc(func(b *bundle.Bundle) { b.Apps["source"].PrivateKey = "secret-ish" }),
		"key wrong type": enc(func(b *bundle.Bundle) {
			b.Apps["source"].PrivateKey = "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n"
		}),
	}
	for name, in := range cases {
		_, err := bundle.Decode(in)
		if err == nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		if strings.Contains(err.Error(), "secret-ish") || strings.Contains(err.Error(), "AAAA") {
			t.Errorf("%s: error leaks key material: %v", name, err)
		}
	}
}

func TestParseKeyFormats(t *testing.T) {
	k := testutil.Key(t)
	for _, pkcs8 := range []bool{false, true} {
		got, err := bundle.ParseKey(testutil.PEM(t, k, pkcs8))
		if err != nil {
			t.Fatalf("pkcs8=%v: %v", pkcs8, err)
		}
		if !got.Equal(k) {
			t.Fatalf("pkcs8=%v: key mismatch", pkcs8)
		}
	}
}
