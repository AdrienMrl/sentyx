package ota

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestSignVerifyAndTamper(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{V: 1, ID: "app-v2", Type: ReleaseApplication, Version: "v2", Sequence: 2,
		ArtifactSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ArtifactSize: 12}
	sig, err := Sign(m, priv)
	if err != nil {
		t.Fatal(err)
	}
	r := SignedRelease{Manifest: m, Signature: sig}
	if err := Verify(r, pub); err != nil {
		t.Fatal(err)
	}
	r.Manifest.Version = "v3"
	if err := Verify(r, pub); err == nil {
		t.Fatal("tampered release verified")
	}
}

func TestRejectUnsafePackage(t *testing.T) {
	m := Manifest{V: 1, ID: "sys-1", Type: ReleaseSystem, Version: "1", Sequence: 1,
		System: &SystemPlan{Packages: []Package{{Name: "ffmpeg;reboot", Version: "1"}}}}
	if err := m.Validate(); err == nil {
		t.Fatal("unsafe package accepted")
	}
}
