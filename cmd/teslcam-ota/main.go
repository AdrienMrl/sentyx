// teslcam-ota is the operator CLI for signing releases and controlling fleet
// rollout campaigns.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/ota"
	"github.com/AdrienMrl/teslcam/internal/tokenfile"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "keygen":
		keygen(os.Args[2:])
	case "bundle-app":
		bundleApp(os.Args[2:])
	case "sign-system":
		signSystem(os.Args[2:])
	case "publish":
		publish(os.Args[2:])
	case "campaign":
		campaign(os.Args[2:])
	case "campaign-update":
		campaignUpdate(os.Args[2:])
	case "status":
		status(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: teslcam-ota <keygen|bundle-app|sign-system|publish|campaign|campaign-update|status> [flags]")
	os.Exit(2)
}

func keygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "key prefix (writes .private.pem and .public.pem)")
	fs.Parse(args)
	if *out == "" {
		log.Fatal("-out is required")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	pubPEM, privPEM, err := ota.EncodeKeyPair(pub, priv)
	if err != nil {
		log.Fatal(err)
	}
	if err := exclusiveWrite(*out+".private.pem", privPEM, 0o600); err != nil {
		log.Fatal(err)
	}
	if err := exclusiveWrite(*out+".public.pem", pubPEM, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s.private.pem and %s.public.pem\n", *out, *out)
}

func bundleApp(args []string) {
	fs := flag.NewFlagSet("bundle-app", flag.ExitOnError)
	id := fs.String("id", "", "release ID")
	version := fs.String("version", "", "application version")
	sequence := fs.Uint64("sequence", 0, "monotonically increasing application sequence")
	agent := fs.String("agent", "", "linux/arm64 teslcam-agent binary")
	scorer := fs.String("scorer", "", "teslcam-camera-scorer binary")
	key := fs.String("key", "", "Ed25519 private key")
	out := fs.String("out", "", "output .tar.gz path")
	hardware := fs.String("hardware", "pi4", "comma-separated compatible hardware")
	osName := fs.String("os", "trixie", "compatible OS codename")
	fs.Parse(args)
	if *id == "" || *version == "" || *sequence == 0 || *agent == "" || *scorer == "" || *key == "" || *out == "" {
		log.Fatal("-id, -version, -sequence, -agent, -scorer, -key and -out are required")
	}
	if err := makeAppBundle(*out, map[string]string{"teslcam-agent": *agent, "teslcam-camera-scorer": *scorer}); err != nil {
		log.Fatal(err)
	}
	sha, size, err := fileDigest(*out)
	if err != nil {
		log.Fatal(err)
	}
	m := ota.Manifest{V: ota.ProtocolVersion, ID: *id, Type: ota.ReleaseApplication, Version: *version,
		Sequence: *sequence, Hardware: splitNonempty(*hardware), OSCodename: *osName,
		ArtifactSHA256: sha, ArtifactSize: size}
	writeSigned(*out+".release.json", m, *key)
	fmt.Printf("wrote %s and %s.release.json\n", *out, *out)
}

func signSystem(args []string) {
	fs := flag.NewFlagSet("sign-system", flag.ExitOnError)
	in := fs.String("manifest", "", "unsigned system manifest JSON")
	key := fs.String("key", "", "Ed25519 private key")
	out := fs.String("out", "", "signed release JSON")
	fs.Parse(args)
	if *in == "" || *key == "" || *out == "" {
		log.Fatal("-manifest, -key and -out are required")
	}
	b, err := os.ReadFile(*in)
	if err != nil {
		log.Fatal(err)
	}
	var m ota.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		log.Fatal(err)
	}
	writeSigned(*out, m, *key)
}

func writeSigned(out string, m ota.Manifest, keyPath string) {
	priv, err := ota.ReadPrivateKey(keyPath)
	if err != nil {
		log.Fatal(err)
	}
	sig, err := ota.Sign(m, priv)
	if err != nil {
		log.Fatal(err)
	}
	b, _ := json.MarshalIndent(ota.SignedRelease{Manifest: m, Signature: sig}, "", "  ")
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
}

func publish(args []string) {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	server := fs.String("server", "", "server base URL")
	token := fs.String("token-file", "", "operator token file")
	releasePath := fs.String("release", "", "signed release JSON")
	artifact := fs.String("artifact", "", "application artifact; omit for system releases")
	fs.Parse(args)
	client, base, auth := api(*server, *token)
	b, err := os.ReadFile(*releasePath)
	if err != nil {
		log.Fatal(err)
	}
	request(client, http.MethodPost, base+"/v1/ota/releases", auth, "application/json", bytes.NewReader(b), int64(len(b)))
	var rel ota.SignedRelease
	if err := json.Unmarshal(b, &rel); err != nil {
		log.Fatal(err)
	}
	if rel.Manifest.Type == ota.ReleaseApplication {
		if *artifact == "" {
			log.Fatal("-artifact is required for application releases")
		}
		f, err := os.Open(*artifact)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		st, _ := f.Stat()
		request(client, http.MethodPut, base+"/v1/ota/releases/"+rel.Manifest.ID+"/artifact", auth, "application/gzip", f, st.Size())
	}
	fmt.Println("published", rel.Manifest.ID)
}

func campaign(args []string) {
	fs := flag.NewFlagSet("campaign", flag.ExitOnError)
	server := fs.String("server", "", "server base URL")
	token := fs.String("token-file", "", "operator token file")
	release := fs.String("release", "", "release ID")
	percent := fs.Int("percent", 0, "rollout percentage")
	devices := fs.String("devices", "", "comma-separated explicit device IDs")
	fs.Parse(args)
	client, base, auth := api(*server, *token)
	body, _ := json.Marshal(map[string]any{"releaseId": *release, "rolloutPercent": *percent, "deviceIds": splitNonempty(*devices)})
	resp := request(client, http.MethodPost, base+"/v1/ota/campaigns", auth, "application/json", bytes.NewReader(body), int64(len(body)))
	io.Copy(os.Stdout, resp.Body)
}

func campaignUpdate(args []string) {
	fs := flag.NewFlagSet("campaign-update", flag.ExitOnError)
	server := fs.String("server", "", "server base URL")
	token := fs.String("token-file", "", "operator token file")
	id := fs.String("id", "", "campaign ID")
	percentText := fs.String("percent", "", "new rollout percentage")
	state := fs.String("state", "", "active, paused or cancelled")
	fs.Parse(args)
	body := map[string]any{}
	if *percentText != "" {
		n, err := strconv.Atoi(*percentText)
		if err != nil {
			log.Fatal(err)
		}
		body["rolloutPercent"] = n
	}
	if *state != "" {
		body["state"] = *state
	}
	b, _ := json.Marshal(body)
	client, base, auth := api(*server, *token)
	request(client, http.MethodPatch, base+"/v1/ota/campaigns/"+*id, auth, "application/json", bytes.NewReader(b), int64(len(b)))
}

func status(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	server := fs.String("server", "", "server base URL")
	token := fs.String("token-file", "", "operator token file")
	fs.Parse(args)
	client, base, auth := api(*server, *token)
	resp := request(client, http.MethodGet, base+"/v1/ota/campaigns", auth, "", nil, 0)
	io.Copy(os.Stdout, resp.Body)
}

func api(server, tokenPath string) (*http.Client, string, string) {
	if server == "" || tokenPath == "" {
		log.Fatal("-server and -token-file are required")
	}
	tok, err := tokenfile.Read(tokenPath)
	if err != nil {
		log.Fatal(err)
	}
	return &http.Client{Timeout: 15 * time.Minute}, strings.TrimSuffix(server, "/"), "Bearer " + tok
}

func request(client *http.Client, method, url, auth, contentType string, body io.Reader, size int64) *http.Response {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("Authorization", auth)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if size > 0 {
		req.ContentLength = size
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		log.Fatalf("%s %s: %s: %s", method, url, resp.Status, b)
	}
	return resp
}

func makeAppBundle(out string, files map[string]string) error {
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"teslcam-agent", "teslcam-camera-scorer"} {
		src, err := os.Open(files[name])
		if err != nil {
			return err
		}
		st, err := src.Stat()
		if err != nil {
			src.Close()
			return err
		}
		h := &tar.Header{Name: name, Mode: 0o755, Size: st.Size(), ModTime: time.Unix(0, 0)}
		if err := tw.WriteHeader(h); err != nil {
			src.Close()
			return err
		}
		if _, err := io.Copy(tw, src); err != nil {
			src.Close()
			return err
		}
		src.Close()
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}

func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func exclusiveWrite(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func splitNonempty(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
