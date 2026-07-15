// Package teslaauth implements the Tesla SSO OAuth2 authorization-code flow
// with PKCE used to obtain a bearer token that dashcam.tesla.com accepts.
//
// It uses the long-standing first-party "ownerapi" public client, which needs
// no app registration (unlike the Fleet API). The login is browser-assisted:
// the user authenticates at auth.tesla.com and pastes back the redirect URL,
// from which we extract the authorization code and exchange it for tokens.
//
// Tokens are JWTs; the access token is sent as `Authorization: Bearer <token>`.
// The refresh token (granted via the offline_access scope) lets us mint fresh
// access tokens without re-login until it is revoked.
package teslaauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	authorizeURL = "https://auth.tesla.com/oauth2/v3/authorize"
	tokenURL     = "https://auth.tesla.com/oauth2/v3/token"
	// redirectURI is a blank Tesla-hosted callback page. We cannot run a server
	// on it, so the user copies the resulting URL from the address bar; this is
	// the community-standard flow for the ownerapi client.
	redirectURI = "https://auth.tesla.com/void/callback"
	clientID    = "ownerapi"
	scope       = "openid email offline_access"
)

// Token is the subset of the OAuth token response we persist and use.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
}

// Valid reports whether the access token is present and not within skew of expiry.
func (t *Token) Valid() bool {
	if t == nil || t.AccessToken == "" {
		return false
	}
	return time.Now().Add(60 * time.Second).Before(t.Expiry)
}

// Login holds the transient state of an in-progress PKCE authorization.
type Login struct {
	AuthorizeURL string
	verifier     string
	state        string
}

func randomURLSafe(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewLogin builds the authorize URL the user must open in a browser, along with
// the PKCE verifier retained for the code exchange.
func NewLogin() (*Login, error) {
	verifier, err := randomURLSafe(48)
	if err != nil {
		return nil, fmt.Errorf("generate PKCE verifier: %w", err)
	}
	state, err := randomURLSafe(16)
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", scope)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("prompt", "login")

	return &Login{
		AuthorizeURL: authorizeURL + "?" + q.Encode(),
		verifier:     verifier,
		state:        state,
	}, nil
}

// Exchange parses the pasted redirect URL, verifies state, and exchanges the
// authorization code for tokens.
func (l *Login) Exchange(ctx context.Context, httpc *http.Client, redirectedURL string) (*Token, error) {
	u, err := url.Parse(strings.TrimSpace(redirectedURL))
	if err != nil {
		return nil, fmt.Errorf("parse redirect URL: %w", err)
	}
	code := u.Query().Get("code")
	if code == "" {
		return nil, fmt.Errorf("no ?code= found in redirect URL (paste the full address after login)")
	}
	if got := u.Query().Get("state"); got != l.state {
		return nil, fmt.Errorf("state mismatch: got %q, expected %q (possible CSRF or stale login)", got, l.state)
	}

	body := url.Values{}
	body.Set("grant_type", "authorization_code")
	body.Set("client_id", clientID)
	body.Set("code", code)
	body.Set("code_verifier", l.verifier)
	body.Set("redirect_uri", redirectURI)

	return postToken(ctx, httpc, body)
}

// Refresh mints a new access token from a refresh token.
func Refresh(ctx context.Context, httpc *http.Client, refreshToken string) (*Token, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("empty refresh token")
	}
	body := url.Values{}
	body.Set("grant_type", "refresh_token")
	body.Set("client_id", clientID)
	body.Set("refresh_token", refreshToken)
	body.Set("scope", scope)
	return postToken(ctx, httpc, body)
}

func postToken(ctx context.Context, httpc *http.Client, body url.Values) (*Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}
	return &Token{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		TokenType:    out.TokenType,
		Expiry:       time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
	}, nil
}
