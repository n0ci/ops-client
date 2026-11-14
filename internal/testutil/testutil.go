// Package testutil builds throwaway bundles for tests.
package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"sync"
	"testing"

	"github.com/n0ci/ops-client/internal/bundle"
)

var (
	once sync.Once
	key  *rsa.PrivateKey
)

// Key returns a process-wide 2048-bit test key.
func Key(t testing.TB) *rsa.PrivateKey {
	once.Do(func() {
		var err error
		if key, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return key
}

// PEM encodes k as PKCS#1 ("RSA PRIVATE KEY") or PKCS#8 ("PRIVATE KEY").
func PEM(t testing.TB, k *rsa.PrivateKey, pkcs8 bool) string {
	if !pkcs8 {
		return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// Bundle returns a valid bundle struct (source app 123/456, dispatch 124/457).
func Bundle(t testing.TB) *bundle.Bundle {
	k := Key(t)
	return &bundle.Bundle{Version: 1, Owner: "n0ci", Workdir: "~/repo", Apps: map[string]*bundle.App{
		"source":   {AppID: 123, InstallationID: 456, Slug: "n0ci-ops-source", PrivateKey: PEM(t, k, false)},
		"dispatch": {AppID: 124, InstallationID: 457, Slug: "n0ci-ops-dispatch", PrivateKey: PEM(t, k, true)},
	}}
}

// Encoded returns Bundle(t) as an "ops1:" string.
func Encoded(t testing.TB) string {
	s, err := bundle.Encode(Bundle(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Decoded returns Bundle(t) round-tripped through Decode (keys parsed).
func Decoded(t testing.TB) *bundle.Bundle {
	b, err := bundle.Decode(Encoded(t))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
