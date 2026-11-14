package keychain

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := FilePath()
	if _, err := LoadFile(p); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := SaveFile(p, "ops1:abc"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
	if v, err := LoadFile(p); err != nil || v != "ops1:abc" {
		t.Fatalf("load = %q, %v", v, err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(p); err == nil {
		t.Fatal("world-readable bundle file should be refused")
	}
	if filepath.Base(filepath.Dir(p)) != "ops" {
		t.Fatalf("path = %s", p)
	}
}

func TestEnvTakesPrecedence(t *testing.T) {
	t.Setenv(EnvVar, "  ops1:fromenv \n")
	if v, err := Load(); err != nil || v != "ops1:fromenv" {
		t.Fatalf("load = %q, %v", v, err)
	}
}
