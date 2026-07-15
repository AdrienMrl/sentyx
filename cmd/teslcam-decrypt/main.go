// teslcam-decrypt is a proof-of-concept that decrypts a Tesla 2026.20+
// encrypted dashcam clip.
//
// It obtains a bearer token via the Tesla SSO OAuth2 PKCE flow (browser-assisted
// login, cached + auto-refreshed), fetches the clip's per-file AES key from
// dashcam.tesla.com, and decrypts the clip locally. A raw --token can be passed
// instead to skip OAuth entirely.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/dashcam"
	"github.com/AdrienMrl/teslcam/internal/teslaauth"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "", "output path for the decrypted MP4 (required)")
	token := flag.String("token", "", "raw bearer token from dashcam.tesla.com (skips OAuth login)")
	tokenCache := flag.String("token-cache", "", "path to cache OAuth tokens for reuse/refresh")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] ENCRYPTED_CLIP.mp4\n\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	src := flag.Arg(0)
	if *out == "" {
		return fmt.Errorf("-out is required")
	}

	ctx := context.Background()
	httpc := &http.Client{Timeout: 60 * time.Second}

	bearer, err := acquireToken(ctx, httpc, *token, *tokenCache)
	if err != nil {
		return err
	}

	hdr, err := dashcam.ReadHeader(src)
	if err != nil {
		return fmt.Errorf("read clip header: %w", err)
	}
	fmt.Printf("clip: id=%s vin=%s key_id=%d plaintext=%d bytes\n", hdr.ID, hdr.VIN, hdr.KeyID, hdr.PlaintextSize)

	keys, err := dashcam.FetchKeys(ctx, httpc, bearer, []*dashcam.Header{hdr})
	if err != nil {
		return fmt.Errorf("fetch key: %w", err)
	}
	key, ok := keys[hdr.ID]
	if !ok {
		return fmt.Errorf("Tesla returned no key for clip %s", hdr.ID)
	}

	written, err := dashcam.DecryptClip(src, *out, hdr, key)
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	fmt.Printf("decrypted %d bytes -> %s\n", written, *out)
	return nil
}

// acquireToken resolves a bearer token in priority order: explicit --token, then
// a valid/refreshable cached OAuth token, then an interactive browser login.
func acquireToken(ctx context.Context, httpc *http.Client, raw, cachePath string) (string, error) {
	if raw != "" {
		return raw, nil
	}

	if cachePath != "" {
		if tok := loadToken(cachePath); tok != nil {
			if tok.Valid() {
				return tok.AccessToken, nil
			}
			if tok.RefreshToken != "" {
				fmt.Fprintln(os.Stderr, "refreshing cached token…")
				refreshed, err := teslaauth.Refresh(ctx, httpc, tok.RefreshToken)
				if err == nil {
					saveToken(cachePath, refreshed)
					return refreshed.AccessToken, nil
				}
				fmt.Fprintf(os.Stderr, "refresh failed (%v); falling back to login\n", err)
			}
		}
	}

	tok, err := interactiveLogin(ctx, httpc)
	if err != nil {
		return "", err
	}
	if cachePath != "" {
		saveToken(cachePath, tok)
	}
	return tok.AccessToken, nil
}

func interactiveLogin(ctx context.Context, httpc *http.Client) (*teslaauth.Token, error) {
	login, err := teslaauth.NewLogin()
	if err != nil {
		return nil, err
	}
	fmt.Fprint(os.Stderr, "\n1. Open this URL in a browser and log in to your Tesla account:\n\n")
	fmt.Fprintln(os.Stderr, "   "+login.AuthorizeURL)
	fmt.Fprintln(os.Stderr, "\n2. After login you'll land on a blank 'Page Not Found' page.")
	fmt.Fprint(os.Stderr, "   Copy the full URL from the address bar and paste it here.\n\n")
	fmt.Fprint(os.Stderr, "Redirect URL: ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read redirect URL: %w", err)
	}
	tok, err := login.Exchange(ctx, httpc, strings.TrimSpace(line))
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}
	fmt.Fprintln(os.Stderr, "login OK")
	return tok, nil
}

func loadToken(path string) *teslaauth.Token {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var tok teslaauth.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil
	}
	return &tok
}

func saveToken(path string, tok *teslaauth.Token) {
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not cache token: %v\n", err)
	}
}
