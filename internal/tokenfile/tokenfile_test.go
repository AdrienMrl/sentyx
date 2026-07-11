package tokenfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadEmptyPathDisablesAuth(t *testing.T) {
	token, err := Read("")
	if err != nil || token != "" {
		t.Fatalf("Read(\"\") = %q, %v; want \"\", nil", token, err)
	}
}

func TestReadTrimsWhitespace(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("  s3cret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if token != "s3cret-token" {
		t.Fatalf("Read = %q, want s3cret-token", token)
	}
}

func TestReadRejectsBlankFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); err == nil {
		t.Fatal("expected error for blank token file")
	}
}

func TestReadMissingFile(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing token file")
	}
}
