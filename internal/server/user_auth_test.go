package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// jwtTestKey is an ES256 signing key plus the JWKS server that publishes it.
type jwtTestKey struct {
	key    *ecdsa.PrivateKey
	kid    string
	issuer string
}

// newJWTTestEnv starts a JWKS httptest server and returns a signer plus a
// Server wired to verify against it. The issuer is fixed for the test.
func newJWTTestEnv(t *testing.T) (*jwtTestKey, *Server, *httptest.Server) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tk := &jwtTestKey{key: key, kid: "test-kid", issuer: "https://proj.supabase.co/auth/v1"}

	jwks := map[string]any{"keys": []map[string]string{{
		"kty": "EC",
		"crv": "P-256",
		"kid": tk.kid,
		"alg": "ES256",
		"x":   b64url(key.PublicKey.X.FillBytes(make([]byte, 32))),
		"y":   b64url(key.PublicKey.Y.FillBytes(make([]byte, 32))),
	}}}
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(jwksSrv.Close)

	c, err := New(Config{
		DataDir:         t.TempDir(),
		ListenAddr:      "127.0.0.1:0",
		Token:           "op-token",
		SupabaseJWKSURL: jwksSrv.URL,
		SupabaseIssuer:  tk.issuer,
	})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(c.Handler())
	t.Cleanup(api.Close)
	return tk, c, api
}

// mint builds and ES256-signs a JWT with the given claims, using kidOverride
// (empty = the real kid) and signKey (nil = the real key).
func (tk *jwtTestKey) mint(t *testing.T, sub, email string, exp time.Time, iss, aud, kidOverride string, signKey *ecdsa.PrivateKey) string {
	t.Helper()
	kid := tk.kid
	if kidOverride != "" {
		kid = kidOverride
	}
	sk := tk.key
	if signKey != nil {
		sk = signKey
	}
	hdr := b64json(t, map[string]any{"alg": "ES256", "typ": "JWT", "kid": kid})
	claims := b64json(t, map[string]any{
		"iss": iss, "sub": sub, "email": email, "aud": aud, "exp": exp.Unix(),
	})
	signingInput := hdr + "." + claims
	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, sk, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return signingInput + "." + b64url(sig)
}

// valid mints a standard, currently-valid token for sub/email.
func (tk *jwtTestKey) valid(t *testing.T, sub, email string) string {
	return tk.mint(t, sub, email, time.Now().Add(time.Hour), tk.issuer, jwtAudience, "", nil)
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func b64json(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b64url(b)
}

// reqFn issues a request and returns the status + body.
func reqFn(t *testing.T, base string) func(method, path, auth string, body []byte) (int, []byte) {
	return func(method, path, auth string, body []byte) (int, []byte) {
		t.Helper()
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, base+path, r)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, b
	}
}

func TestJWTValidTokenUpsertsUser(t *testing.T) {
	tk, c, api := newJWTTestEnv(t)
	do := reqFn(t, api.URL)

	tok := tk.valid(t, "user-1", "alice@example.com")
	// Any authed endpoint exercises the middleware; /events is simplest.
	if code, b := do(http.MethodGet, "/events", "Bearer "+tok, nil); code != http.StatusOK {
		t.Fatalf("GET /events with valid JWT = %d (%s), want 200", code, b)
	}

	var email string
	if err := c.store.db.QueryRow(`SELECT email FROM users WHERE id = ?`, "user-1").Scan(&email); err != nil {
		t.Fatalf("user not upserted: %v", err)
	}
	if email != "alice@example.com" {
		t.Fatalf("stored email = %q, want alice@example.com", email)
	}

	// Email refresh on re-auth.
	tok2 := tk.valid(t, "user-1", "alice2@example.com")
	if code, _ := do(http.MethodGet, "/events", "Bearer "+tok2, nil); code != http.StatusOK {
		t.Fatalf("second GET = %d, want 200", code)
	}
	if err := c.store.db.QueryRow(`SELECT email FROM users WHERE id = ?`, "user-1").Scan(&email); err != nil {
		t.Fatal(err)
	}
	if email != "alice2@example.com" {
		t.Fatalf("email not refreshed: %q", email)
	}
}

func TestJWTRejections(t *testing.T) {
	tk, _, api := newJWTTestEnv(t)
	do := reqFn(t, api.URL)

	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cases := []struct {
		name string
		tok  string
	}{
		{"expired", tk.mint(t, "u", "e", time.Now().Add(-time.Minute), tk.issuer, jwtAudience, "", nil)},
		{"bad signature", tk.mint(t, "u", "e", time.Now().Add(time.Hour), tk.issuer, jwtAudience, "", otherKey)},
		{"wrong issuer", tk.mint(t, "u", "e", time.Now().Add(time.Hour), "https://evil.example/auth/v1", jwtAudience, "", nil)},
		{"wrong audience", tk.mint(t, "u", "e", time.Now().Add(time.Hour), tk.issuer, "anon", "", nil)},
		{"unknown kid", tk.mint(t, "u", "e", time.Now().Add(time.Hour), tk.issuer, jwtAudience, "nope-kid", nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := do(http.MethodGet, "/events", "Bearer "+tc.tok, nil); code != http.StatusUnauthorized {
				t.Fatalf("%s token = %d, want 401", tc.name, code)
			}
		})
	}
}

func TestJWTDeviceAndEventOwnership(t *testing.T) {
	tk, c, api := newJWTTestEnv(t)
	do := reqFn(t, api.URL)

	user1 := tk.valid(t, "user-1", "alice@example.com")
	user2 := tk.valid(t, "user-2", "bob@example.com")

	// user1 registers deviceA; POST /v1/devices must set owner_user_id.
	if code, b := do(http.MethodPost, "/v1/devices", "Bearer "+user1, []byte(`{"deviceId":"deviceA","name":"Alice Pi"}`)); code != http.StatusCreated {
		t.Fatalf("user1 register deviceA = %d (%s), want 201", code, b)
	}
	if owner, exists, err := c.store.deviceOwner("deviceA"); err != nil || !exists || owner != "user-1" {
		t.Fatalf("deviceA owner = %q exists=%v err=%v, want user-1", owner, exists, err)
	}
	// user2 registers deviceB.
	if code, _ := do(http.MethodPost, "/v1/devices", "Bearer "+user2, []byte(`{"deviceId":"deviceB","name":"Bob Pi"}`)); code != http.StatusCreated {
		t.Fatalf("user2 register deviceB, want 201")
	}

	// user2 cannot re-register deviceA (owned by user1).
	if code, _ := do(http.MethodPost, "/v1/devices", "Bearer "+user2, []byte(`{"deviceId":"deviceA","name":"steal"}`)); code != http.StatusForbidden {
		t.Fatalf("user2 re-register deviceA = %d, want 403", code)
	}
	// user1 can re-register its own device (rotation).
	if code, _ := do(http.MethodPost, "/v1/devices", "Bearer "+user1, []byte(`{"deviceId":"deviceA","name":"Alice Pi 2"}`)); code != http.StatusCreated {
		t.Fatalf("user1 re-register own device, want 201")
	}

	// user1 cannot read user2's device; can read its own.
	if code, _ := do(http.MethodGet, "/v1/devices/deviceB", "Bearer "+user1, nil); code != http.StatusForbidden {
		t.Fatalf("user1 read deviceB = %d, want 403", code)
	}
	if code, _ := do(http.MethodGet, "/v1/devices/deviceA", "Bearer "+user1, nil); code != http.StatusOK {
		t.Fatalf("user1 read own deviceA, want 200")
	}
	// Operator reads any.
	if code, _ := do(http.MethodGet, "/v1/devices/deviceB", "Bearer op-token", nil); code != http.StatusOK {
		t.Fatalf("operator read deviceB, want 200")
	}

	// Ingest an event for each device (operator token).
	putEvent := func(key, deviceID string) {
		body := []byte(`{"device_id":"` + deviceID + `","source":{"type":"tesla_sentry","directory_name":"` + key + `"}}`)
		if code, b := do(http.MethodPut, "/v1/events/"+deviceID+":"+key, "Bearer op-token", body); code != http.StatusOK {
			t.Fatalf("ingest %s = %d (%s)", key, code, b)
		}
	}
	putEvent("2026-07-16_10-00-00", "deviceA")
	putEvent("2026-07-16_11-00-00", "deviceB")
	evA, evB := "deviceA:2026-07-16_10-00-00", "deviceB:2026-07-16_11-00-00"

	// GET /events: user1 sees only deviceA's event.
	code, b := do(http.MethodGet, "/events", "Bearer "+user1, nil)
	if code != http.StatusOK {
		t.Fatalf("user1 GET /events = %d", code)
	}
	var list []EventSummary
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != evA {
		t.Fatalf("user1 /events = %+v, want only %s", list, evA)
	}
	// Operator sees both.
	_, b = do(http.MethodGet, "/events", "Bearer op-token", nil)
	json.Unmarshal(b, &list)
	if len(list) != 2 {
		t.Fatalf("operator /events len = %d, want 2", len(list))
	}

	// GET /events/{id}: user1 own event 200, cross-user 403.
	if code, _ := do(http.MethodGet, "/events/"+evA, "Bearer "+user1, nil); code != http.StatusOK {
		t.Fatalf("user1 read own event = %d, want 200", code)
	}
	if code, _ := do(http.MethodGet, "/events/"+evB, "Bearer "+user1, nil); code != http.StatusForbidden {
		t.Fatalf("user1 read deviceB event = %d, want 403", code)
	}
	// thumb honors the same rule (403 before any file lookup).
	if code, _ := do(http.MethodGet, "/events/"+evB+"/thumb", "Bearer "+user1, nil); code != http.StatusForbidden {
		t.Fatalf("user1 read deviceB thumb = %d, want 403", code)
	}

	// /usage is operator-only.
	if code, _ := do(http.MethodGet, "/usage", "Bearer "+user1, nil); code != http.StatusForbidden {
		t.Fatalf("user1 GET /usage = %d, want 403", code)
	}
	if code, _ := do(http.MethodGet, "/usage", "Bearer op-token", nil); code != http.StatusOK {
		t.Fatalf("operator GET /usage, want 200")
	}
}

func TestJWTDisabledByDefault(t *testing.T) {
	// With no Supabase config, a JWT-shaped credential is treated as an
	// (unknown) opaque token and rejected — JWT verification never runs.
	c, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Token: "op-token"})
	if err != nil {
		t.Fatal(err)
	}
	if c.jwt != nil {
		t.Fatal("jwt verifier should be nil when unconfigured")
	}
	api := httptest.NewServer(c.Handler())
	defer api.Close()
	do := reqFn(t, api.URL)
	if code, _ := do(http.MethodGet, "/events", "Bearer aaa.bbb.ccc", nil); code != http.StatusUnauthorized {
		t.Fatalf("JWT-shaped token with JWT auth off = %d, want 401", code)
	}
}

func TestServerConfigRequiresBothSupabaseFlags(t *testing.T) {
	if _, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", SupabaseIssuer: "https://x/auth/v1"}); err == nil {
		t.Fatal("issuer without JWKS URL should error")
	}
	if _, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", SupabaseJWKSURL: "https://x/jwks"}); err == nil {
		t.Fatal("JWKS URL without issuer should error")
	}
}

func TestJWTUserCannotIngest(t *testing.T) {
	tk, _, api := newJWTTestEnv(t)
	do := reqFn(t, api.URL)
	user := tk.valid(t, "user-1", "alice@example.com")

	evBody := []byte(`{"device_id":"d","source":{"type":"tesla_sentry","directory_name":"x"}}`)
	cases := []struct {
		method, path string
		body         []byte
	}{
		{http.MethodPut, "/v1/events/d:x", evBody},
		{http.MethodPut, "/v1/blobs/" + strings.Repeat("a", 64), []byte("data")},
		{http.MethodPut, "/v1/events/d:x/manifests/1", []byte(`{"generation":1}`)},
		{http.MethodPost, "/v1/events/d:x/manifests/1/finalize", nil},
	}
	for _, tc := range cases {
		if code, b := do(tc.method, tc.path, "Bearer "+user, tc.body); code != http.StatusForbidden {
			t.Fatalf("%s %s with user JWT = %d (%s), want 403", tc.method, tc.path, code, b)
		}
	}

	// The v1 event read applies ownership: an event on an unowned device is
	// invisible to a user, readable by the operator.
	if code, _ := do(http.MethodPut, "/v1/events/d:x", "Bearer op-token", evBody); code != http.StatusOK {
		t.Fatal("operator event upsert should succeed")
	}
	if code, _ := do(http.MethodGet, "/v1/events/d:x", "Bearer "+user, nil); code != http.StatusForbidden {
		t.Fatal("user read of unowned v1 event should be 403")
	}
	if code, _ := do(http.MethodGet, "/v1/events/d:x", "Bearer op-token", nil); code != http.StatusOK {
		t.Fatal("operator read of v1 event should be 200")
	}
}
