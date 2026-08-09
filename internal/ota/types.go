// Package ota defines the signed fleet-update protocol shared by the server,
// release tooling and the updater installed on field units.
package ota

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const ProtocolVersion = 1

// BundleArgsName is the agent argument file carried inside an application
// bundle. It sits beside the binaries so the `current` symlink swaps a release
// and its flags atomically — an OTA release cannot replace a systemd unit, so
// any flag that lived in the unit could never reach a deployed unit.
const BundleArgsName = "agent.args"

type ReleaseType string

const (
	ReleaseApplication ReleaseType = "application"
	ReleaseSystem      ReleaseType = "system"
)

// Manifest is the signed, immutable description of a release. Signature is
// deliberately outside this struct: json.Marshal(Manifest) is the canonical
// byte representation signed by release tooling and verified by every Pi.
type Manifest struct {
	V              int               `json:"v"`
	ID             string            `json:"id"`
	Type           ReleaseType       `json:"type"`
	Version        string            `json:"version"`
	Sequence       uint64            `json:"sequence"`
	Hardware       []string          `json:"hardware,omitempty"`
	OSCodename     string            `json:"osCodename,omitempty"`
	ArtifactSHA256 string            `json:"artifactSha256,omitempty"`
	ArtifactSize   int64             `json:"artifactSize,omitempty"`
	System         *SystemPlan       `json:"system,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type SystemPlan struct {
	Packages    []Package `json:"packages"`
	BackupPaths []string  `json:"backupPaths,omitempty"`
	PreInstall  []string  `json:"preInstall,omitempty"`
	PostInstall []string  `json:"postInstall,omitempty"`
	Reboot      bool      `json:"reboot"`
	AllowLTE    bool      `json:"allowLTE,omitempty"`
	MaxAttempts int       `json:"maxAttempts,omitempty"`
}

type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type SignedRelease struct {
	Manifest  Manifest `json:"manifest"`
	Signature string   `json:"signature"` // base64 Ed25519 signature
	Artifact  string   `json:"artifactUrl,omitempty"`
}

type Plan struct {
	Release    SignedRelease `json:"release"`
	CampaignID string        `json:"campaignId"`
}

type DeviceStatus struct {
	V           int    `json:"v"`
	ReleaseID   string `json:"releaseId"`
	CampaignID  string `json:"campaignId,omitempty"`
	State       string `json:"state"`
	ProgressPct int    `json:"progressPct,omitempty"`
	Error       string `json:"error,omitempty"`
	AppVersion  string `json:"appVersion,omitempty"`
	OSVersion   string `json:"osVersion,omitempty"`
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)

func (m Manifest) Validate() error {
	if m.V != ProtocolVersion {
		return fmt.Errorf("ota: manifest v must be %d", ProtocolVersion)
	}
	if !safeID.MatchString(m.ID) || !safeID.MatchString(m.Version) {
		return errors.New("ota: invalid release id or version")
	}
	if m.Sequence == 0 {
		return errors.New("ota: sequence must be greater than zero")
	}
	switch m.Type {
	case ReleaseApplication:
		if m.System != nil {
			return errors.New("ota: application release must not contain a system plan")
		}
		if len(m.ArtifactSHA256) != 64 || m.ArtifactSize <= 0 {
			return errors.New("ota: application release requires artifact sha256 and size")
		}
	case ReleaseSystem:
		if m.System == nil || len(m.System.Packages) == 0 {
			return errors.New("ota: system release requires at least one package")
		}
		for _, p := range m.System.Packages {
			if !packageName.MatchString(p.Name) || p.Version == "" || strings.HasPrefix(p.Version, "-") || strings.ContainsAny(p.Version, " \t\r\n") {
				return fmt.Errorf("ota: invalid package pin %q=%q", p.Name, p.Version)
			}
		}
	default:
		return fmt.Errorf("ota: unknown release type %q", m.Type)
	}
	for _, h := range m.Hardware {
		if !safeID.MatchString(h) {
			return fmt.Errorf("ota: invalid hardware identifier %q", h)
		}
	}
	return nil
}

func Sign(m Manifest, key ed25519.PrivateKey) (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, b)), nil
}

func Verify(r SignedRelease, key ed25519.PublicKey) error {
	if err := r.Manifest.Validate(); err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(r.Signature)
	if err != nil {
		return fmt.Errorf("ota: decode signature: %w", err)
	}
	b, err := json.Marshal(r.Manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, b, sig) {
		return errors.New("ota: invalid release signature")
	}
	return nil
}

func ReadPublicKey(path string) (ed25519.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block != nil {
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		pub, ok := key.(ed25519.PublicKey)
		if !ok {
			return nil, errors.New("ota: public key is not Ed25519")
		}
		return pub, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("ota: public key must be PKIX PEM or base64 Ed25519")
	}
	return ed25519.PublicKey(raw), nil
}

func ReadPrivateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("ota: private key must be PKCS8 PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("ota: private key is not Ed25519")
	}
	return priv, nil
}

func EncodeKeyPair(pub ed25519.PublicKey, priv ed25519.PrivateKey) (publicPEM, privatePEM []byte, err error) {
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	publicPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	privatePEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	return publicPEM, privatePEM, nil
}
