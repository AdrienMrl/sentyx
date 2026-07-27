// tesla-fleet-auth runs Tesla's official Fleet API OAuth flows and keeps the
// resulting tokens in ~/.teslcam/tesla-fleet.json.
//
// Why this exists: the clip-download POC (experiments/tesla-clip) currently runs
// on an `ownerapi` token captured out of Tesla's own Android app with mitmproxy.
// That is not a credential any product can mint. Fleet API third-party OAuth is
// the sanctioned replacement, and this tool proves it end to end before any of it
// is wired into the phone app.
//
// The token endpoint advertises only `client_secret_post` (see the OIDC discovery
// document), so there is no PKCE-style public client: the code exchange needs the
// client secret and therefore belongs on a server, never on a handset. This CLI
// stands in for that server while we are proving the flow.
//
// Subcommands:
//
//	login          authorization-code flow -> access + refresh token
//	refresh        rotate the stored refresh token
//	token          print a valid access token (refreshing if it has expired)
//	partner-token  client-credentials token, for the partner_accounts endpoints
//	partner-check  read back the public key Tesla has on file for our domain
//	partner-register  one-time registration of our domain with Tesla
//	vehicles       GET /api/1/vehicles, i.e. does this token actually work
package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	authorizeURL = "https://auth.tesla.com/oauth2/v3/authorize"
	tokenURL     = "https://fleet-auth.prd.vn.cloud.tesla.com/oauth2/v3/token"

	// Region host for North America / Asia-Pacific accounts. EU accounts use
	// fleet-api.prd.eu.vn.cloud.tesla.com; the API answers HTTP 421 with the
	// correct host if this is wrong.
	fleetAPIBase = "https://fleet-api.prd.na.vn.cloud.tesla.com"

	// vehicle_cmds is the one that matters for the clip work: signed_command is a
	// vehicle command as far as Fleet API is concerned. vehicle_device_data covers
	// the /vehicles reads used to check the car is awake.
	scopes = "openid offline_access vehicle_device_data vehicle_cmds vehicle_location"

	// Partner tokens are scoped to the endpoints that manage the app itself.
	partnerScopes = "openid vehicle_device_data vehicle_cmds"
)

// config is read from the environment. Every field is required — a wrong value
// silently falling back to a default is how OAuth debugging turns into a
// multi-hour exercise, so absent means fail.
type config struct {
	clientID     string
	clientSecret string
	redirectURI  string
	domain       string
}

func loadConfig(need ...string) (config, error) {
	var c config
	var missing []string
	get := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}
	for _, k := range need {
		switch k {
		case "TESLA_CLIENT_ID":
			c.clientID = get(k)
		case "TESLA_CLIENT_SECRET":
			c.clientSecret = get(k)
		case "TESLA_REDIRECT_URI":
			c.redirectURI = get(k)
		case "TESLA_APP_DOMAIN":
			c.domain = get(k)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing required environment variable(s): %s", strings.Join(missing, ", "))
	}
	return c, nil
}

// tokens is what we persist. ObtainedAt + ExpiresIn beats storing an absolute
// deadline because it survives a clock change on the machine.
type tokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token,omitempty"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int       `json:"expires_in"`
	ObtainedAt   time.Time `json:"obtained_at"`
	Scope        string    `json:"scope,omitempty"`
}

func (t tokens) expired() bool {
	// 60 s of slack so a token does not expire mid-request.
	return time.Now().After(t.ObtainedAt.Add(time.Duration(t.ExpiresIn)*time.Second - 60*time.Second))
}

func storePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".teslcam", "tesla-fleet.json"), nil
}

func saveTokens(t tokens) error {
	p, err := storePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s (expires in %s)\n", p, time.Duration(t.ExpiresIn)*time.Second)
	return nil
}

func loadTokens() (tokens, error) {
	var t tokens
	p, err := storePath()
	if err != nil {
		return t, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return t, fmt.Errorf("read %s: %w (run `tesla-fleet-auth login` first)", p, err)
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("parse %s: %w", p, err)
	}
	if t.RefreshToken == "" {
		return t, fmt.Errorf("%s has no refresh_token — was offline_access in the scope list?", p)
	}
	return t, nil
}

// postForm posts a form to Tesla's token endpoint and decodes the reply, turning
// a non-2xx into an error that carries the body (Tesla's OAuth errors are
// descriptive and worth surfacing verbatim).
func postForm(endpoint string, form url.Values) (tokens, error) {
	var t tokens
	resp, err := http.PostForm(endpoint, form)
	if err != nil {
		return t, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return t, err
	}
	if resp.StatusCode/100 != 2 {
		return t, fmt.Errorf("%s -> HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return t, fmt.Errorf("parse token response: %w (body: %s)", err, body)
	}
	t.ObtainedAt = time.Now()
	return t, nil
}

// randomState is CSRF protection for the authorize redirect.
func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

func cmdLogin() error {
	cfg, err := loadConfig("TESLA_CLIENT_ID", "TESLA_CLIENT_SECRET", "TESLA_REDIRECT_URI")
	if err != nil {
		return err
	}
	state, err := randomState()
	if err != nil {
		return err
	}

	q := url.Values{
		"client_id":     {cfg.clientID},
		"redirect_uri":  {cfg.redirectURI},
		"response_type": {"code"},
		"scope":         {scopes},
		"state":         {state},
	}
	authURL := authorizeURL + "?" + q.Encode()

	// Two ways to catch the redirect. If the app is registered with a localhost
	// callback we can listen for it; otherwise the redirect lands on a server we
	// are not running here, so the operator pastes the URL back.
	u, err := url.Parse(cfg.redirectURI)
	if err != nil {
		return fmt.Errorf("parse TESLA_REDIRECT_URI: %w", err)
	}
	var code string
	if host := u.Hostname(); host == "localhost" || host == "127.0.0.1" {
		code, err = listenForCode(u, authURL, state)
	} else {
		code, err = pasteCode(authURL, state)
	}
	if err != nil {
		return err
	}

	t, err := postForm(tokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {cfg.clientID},
		"client_secret": {cfg.clientSecret},
		"code":          {code},
		"redirect_uri":  {cfg.redirectURI},
		"audience":      {fleetAPIBase},
	})
	if err != nil {
		return err
	}
	return saveTokens(t)
}

// listenForCode serves the registered localhost callback exactly once.
func listenForCode(u *url.URL, authURL, state string) (string, error) {
	port := u.Port()
	if port == "" {
		return "", fmt.Errorf("TESLA_REDIRECT_URI %q has no port; a localhost callback needs an explicit one", u)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return "", fmt.Errorf("listen on %s: %w", port, err)
	}
	defer ln.Close()

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != u.Path {
			http.NotFound(w, r)
			return
		}
		qs := r.URL.Query()
		if e := qs.Get("error"); e != "" {
			fmt.Fprintf(w, "Tesla returned an error: %s %s", e, qs.Get("error_description"))
			done <- result{err: fmt.Errorf("authorize: %s: %s", e, qs.Get("error_description"))}
			return
		}
		if got := qs.Get("state"); got != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			done <- result{err: fmt.Errorf("state mismatch: expected %q, got %q", state, got)}
			return
		}
		fmt.Fprint(w, "Authorized. You can close this tab and return to the terminal.")
		done <- result{code: qs.Get("code")}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	fmt.Fprintf(os.Stderr, "opening the Tesla consent page; waiting for the callback on %s\n\n%s\n\n", u, authURL)
	open(authURL)

	select {
	case r := <-done:
		return r.code, r.err
	case <-time.After(5 * time.Minute):
		return "", errors.New("timed out waiting for the OAuth callback")
	}
}

// pasteCode is the fallback when the callback lands somewhere we do not control
// from here (e.g. the production https redirect).
func pasteCode(authURL, state string) (string, error) {
	fmt.Fprintf(os.Stderr, "Open this URL, approve, then paste the FULL redirected URL back here:\n\n%s\n\n> ", authURL)
	var line string
	if _, err := fmt.Scanln(&line); err != nil {
		return "", fmt.Errorf("read pasted URL: %w", err)
	}
	u, err := url.Parse(strings.TrimSpace(line))
	if err != nil {
		return "", fmt.Errorf("parse pasted URL: %w", err)
	}
	qs := u.Query()
	if e := qs.Get("error"); e != "" {
		return "", fmt.Errorf("authorize: %s: %s", e, qs.Get("error_description"))
	}
	if got := qs.Get("state"); got != state {
		return "", fmt.Errorf("state mismatch: expected %q, got %q", state, got)
	}
	code := qs.Get("code")
	if code == "" {
		return "", errors.New("pasted URL has no ?code=")
	}
	return code, nil
}

func cmdRefresh() error {
	cfg, err := loadConfig("TESLA_CLIENT_ID")
	if err != nil {
		return err
	}
	old, err := loadTokens()
	if err != nil {
		return err
	}
	// Tesla rotates refresh tokens: the reply carries a NEW one and the old is
	// spent. Persisting immediately is the whole ballgame — lose this write and
	// the user has to re-consent.
	t, err := postForm(tokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {cfg.clientID},
		"refresh_token": {old.RefreshToken},
	})
	if err != nil {
		return err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = old.RefreshToken
	}
	return saveTokens(t)
}

func cmdToken() error {
	t, err := loadTokens()
	if err != nil {
		return err
	}
	if t.expired() {
		fmt.Fprintln(os.Stderr, "access token expired; refreshing")
		if err := cmdRefresh(); err != nil {
			return err
		}
		if t, err = loadTokens(); err != nil {
			return err
		}
	}
	fmt.Println(t.AccessToken)
	return nil
}

func cmdPartnerToken() (string, error) {
	cfg, err := loadConfig("TESLA_CLIENT_ID", "TESLA_CLIENT_SECRET")
	if err != nil {
		return "", err
	}
	t, err := postForm(tokenURL, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {cfg.clientID},
		"client_secret": {cfg.clientSecret},
		"scope":         {partnerScopes},
		"audience":      {fleetAPIBase},
	})
	if err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// apiCall runs an authenticated Fleet API request and prints the reply.
func apiCall(method, path, bearer string, body io.Reader) error {
	req, err := http.NewRequest(method, fleetAPIBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s -> HTTP %d\n%s\n", method, path, resp.StatusCode, indentJSON(b))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func indentJSON(b []byte) []byte {
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		return b
	}
	pretty, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return b
	}
	return pretty
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "login":
		err = cmdLogin()
	case "refresh":
		err = cmdRefresh()
	case "token":
		err = cmdToken()
	case "partner-token":
		var tok string
		if tok, err = cmdPartnerToken(); err == nil {
			fmt.Println(tok)
		}
	case "partner-check":
		// Confirms the .well-known public key Tesla actually holds for our
		// domain, which is the usual reason signed commands fail later.
		var cfg config
		if cfg, err = loadConfig("TESLA_APP_DOMAIN"); err == nil {
			var tok string
			if tok, err = cmdPartnerToken(); err == nil {
				err = apiCall(http.MethodGet, "/api/1/partner_accounts/public_key?domain="+url.QueryEscape(cfg.domain), tok, nil)
			}
		}
	case "partner-register":
		var cfg config
		if cfg, err = loadConfig("TESLA_APP_DOMAIN"); err == nil {
			var tok string
			if tok, err = cmdPartnerToken(); err == nil {
				payload := strings.NewReader(fmt.Sprintf(`{"domain":%q}`, cfg.domain))
				err = apiCall(http.MethodPost, "/api/1/partner_accounts", tok, payload)
			}
		}
	case "vehicles":
		var t tokens
		if t, err = loadTokens(); err == nil {
			err = apiCall(http.MethodGet, "/api/1/vehicles", t.AccessToken, nil)
		}
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `tesla-fleet-auth — Tesla Fleet API OAuth

  login             authorization-code flow, writes ~/.teslcam/tesla-fleet.json
  refresh           rotate the stored refresh token
  token             print a valid access token (refreshes if expired)
  partner-token     client-credentials token for the partner endpoints
  partner-check     show the public key Tesla holds for TESLA_APP_DOMAIN
  partner-register  register TESLA_APP_DOMAIN with Tesla (one-time)
  vehicles          GET /api/1/vehicles with the stored token

Environment (no defaults; unset is an error):
  TESLA_CLIENT_ID      from developer.tesla.com
  TESLA_CLIENT_SECRET  from developer.tesla.com
  TESLA_REDIRECT_URI   must match the app config exactly
  TESLA_APP_DOMAIN     the domain hosting com.tesla.3p.public-key.pem
`)
}

// open best-effort launches the system browser.
func open(u string) {
	_ = exec.Command("open", u).Start()
}
