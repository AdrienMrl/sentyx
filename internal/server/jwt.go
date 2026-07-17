package server

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// jwtAudience is the audience Supabase stamps on authenticated-user tokens.
const jwtAudience = "authenticated"

// jwtMinRefetchInterval bounds how often an unknown key id triggers a JWKS
// refetch, so a stream of bad tokens cannot hammer the JWKS endpoint. A
// successful key lookup never refetches.
const jwtMinRefetchInterval = time.Minute

// jwtVerifier verifies Supabase user JWTs against a cached JWKS. Supabase's
// newer projects sign with ES256 (ECC P-256); older ones with RS256 — both are
// accepted. It validates the signature, issuer, expiry, and audience.
type jwtVerifier struct {
	jwksURL string
	issuer  string
	client  *http.Client

	mu        sync.Mutex
	keys      map[string]crypto.PublicKey // kid -> *ecdsa.PublicKey | *rsa.PublicKey
	fetchedAt time.Time
}

func newJWTVerifier(jwksURL, issuer string) *jwtVerifier {
	return &jwtVerifier{
		jwksURL: jwksURL,
		issuer:  issuer,
		client:  &http.Client{Timeout: 10 * time.Second},
		keys:    map[string]crypto.PublicKey{},
	}
}

// jwtClaims is the subset of verified claims the server acts on.
type jwtClaims struct {
	Subject string
	Email   string
}

// verify checks the token's signature and standard claims, returning the user
// id (sub) and email on success.
func (v *jwtVerifier) verify(token string) (jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, errors.New("token is not a JWT")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeJWTSegment(parts[0], &hdr); err != nil {
		return jwtClaims{}, fmt.Errorf("decoding JWT header: %w", err)
	}
	key, err := v.key(hdr.Kid)
	if err != nil {
		return jwtClaims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwtClaims{}, fmt.Errorf("decoding JWT signature: %w", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch hdr.Alg {
	case "ES256":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return jwtClaims{}, errors.New("key id is not an EC key for ES256")
		}
		if len(sig) != 64 {
			return jwtClaims{}, errors.New("malformed ES256 signature")
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(pub, sum[:], r, s) {
			return jwtClaims{}, errors.New("invalid signature")
		}
	case "RS256":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return jwtClaims{}, errors.New("key id is not an RSA key for RS256")
		}
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
			return jwtClaims{}, errors.New("invalid signature")
		}
	default:
		return jwtClaims{}, fmt.Errorf("unsupported JWT alg %q", hdr.Alg)
	}

	var claims struct {
		Iss   string          `json:"iss"`
		Sub   string          `json:"sub"`
		Email string          `json:"email"`
		Exp   int64           `json:"exp"`
		Aud   json.RawMessage `json:"aud"`
	}
	if err := decodeJWTSegment(parts[1], &claims); err != nil {
		return jwtClaims{}, fmt.Errorf("decoding JWT claims: %w", err)
	}
	if claims.Iss != v.issuer {
		return jwtClaims{}, fmt.Errorf("unexpected issuer %q", claims.Iss)
	}
	if claims.Exp == 0 || time.Now().After(time.Unix(claims.Exp, 0)) {
		return jwtClaims{}, errors.New("token expired")
	}
	if !audienceContains(claims.Aud, jwtAudience) {
		return jwtClaims{}, fmt.Errorf("audience does not contain %q", jwtAudience)
	}
	if claims.Sub == "" {
		return jwtClaims{}, errors.New("missing sub claim")
	}
	return jwtClaims{Subject: claims.Sub, Email: claims.Email}, nil
}

// key returns the cached public key for kid, refetching the JWKS at most once
// per jwtMinRefetchInterval when the kid is unknown (Supabase rotates keys).
func (v *jwtVerifier) key(kid string) (crypto.PublicKey, error) {
	v.mu.Lock()
	k, ok := v.keys[kid]
	last := v.fetchedAt
	v.mu.Unlock()
	if ok {
		return k, nil
	}
	if !last.IsZero() && time.Since(last) < jwtMinRefetchInterval {
		return nil, fmt.Errorf("unknown JWT key id %q", kid)
	}
	if err := v.refresh(); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown JWT key id %q", kid)
}

func (v *jwtVerifier) refresh() error {
	resp, err := v.client.Get(v.jwksURL)
	if err != nil {
		return fmt.Errorf("fetching JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching JWKS: status %d", resp.StatusCode)
	}
	var doc struct {
		Keys []jwkKey `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return fmt.Errorf("parsing JWKS: %w", err)
	}
	keys := map[string]crypto.PublicKey{}
	for _, jk := range doc.Keys {
		pub, err := jk.publicKey()
		if err != nil {
			continue // skip keys we cannot use (unsupported type/curve)
		}
		keys[jk.Kid] = pub
	}
	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}

// jwkKey is one entry of a JWKS document (RFC 7517), limited to the EC/RSA
// fields we consume.
type jwkKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (k jwkKey) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("unsupported EC curve %q", k.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, err
		}
		y, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, err
		}
		return &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(x),
			Y:     new(big.Int).SetBytes(y),
		}, nil
	case "RSA":
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, err
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, err
		}
		exp := 0
		for _, b := range e {
			exp = exp<<8 | int(b)
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}, nil
	default:
		return nil, fmt.Errorf("unsupported key type %q", k.Kty)
	}
}

func decodeJWTSegment(seg string, dst any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// audienceContains reports whether the JWT "aud" claim (a string or array of
// strings) contains want.
func audienceContains(raw json.RawMessage, want string) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == want
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		for _, a := range arr {
			if a == want {
				return true
			}
		}
	}
	return false
}
