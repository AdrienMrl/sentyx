# Tesla OAuth — reference notes

## Decision (2026-07-13): ask users to disable encryption

Encrypted-clip decryption is **not** a product dependency. Firmware 2026.20 makes
encryption opt-out (*Controls → Safety → Encrypt Dashcam Recordings → off*), so
onboarding instructs the user to disable it once and the whole pipeline stays
plaintext — no Tesla API dependency, no token refresh, no per-clip key round-trips.

Rationale: the official Fleet API has **no dashcam/clip endpoint at all** (only
`set_sentry_mode` + telemetry); the only decrypt path is `dashcam.tesla.com`'s
undocumented first-party endpoint, which is fragile and can break on any update.
teslausb (the most mature Pi dashcam project, issue #1049) reached the same
conclusion — their answer is also "turn encryption off in the car".

Tesla OAuth still earns a place for **product login / account creation** via the
official OIDC flow (§1) — that is decoupled from the clip problem and worth building.

**Keep as break-glass, not shipped:** `internal/dashcam` + `cmd/teslcam-decrypt`
(reverse-engineered decrypt) and one encrypted test clip in `testdata/encrypted/`.
Also keep an encrypted-container **detection tripwire** in the pipeline (signature:
metadata offset `0x1000` at file byte `0x14`) so a future forced-encryption
firmware surfaces as a clear alert instead of silent failure.

### Contingency if Tesla ever removes the opt-out

1. Decrypt on the Pi via the reverse-engineered endpoint (token refresh on-device).
2. Or decrypt server-side (Pi uploads encrypted container + metadata; VPS unwraps).
3. Migrate to Fleet API if Tesla ever ships an official clip endpoint.
4. Worst case (encryption forced *and* endpoint locked down): live analysis dies,
   fall back to manual drive-pull + Tesla's viewer.

---

Two distinct Tesla auth surfaces are relevant to this project. They use the same
`auth.tesla.com` login pages but issue tokens with **different audiences**, and
they are not interchangeable.

| Concern | Auth surface | Status |
|---|---|---|
| Product login / user account creation | Fleet API third-party OIDC | Official, supported |
| Encrypted dashcam clip decryption | `dashcam.tesla.com` first-party endpoint | Undocumented, reverse-engineered |

---

## 1. Fleet API third-party OAuth (official OIDC)

Tesla's Fleet API third-party authentication is a standard OpenID Connect
provider. This is the correct basis for a "Sign in with Tesla" login page and for
creating user accounts.

### Discovery document

`https://fleet-auth.prd.vn.cloud.tesla.com/oauth2/v3/thirdparty/.well-known/openid-configuration`

```json
{
  "authorization_endpoint": "https://auth.tesla.com/oauth2/v3/authorize",
  "token_endpoint":         "https://auth.tesla.com/oauth2/v3/token",
  "userinfo_endpoint":      "https://auth.tesla.com/oauth2/v3/userinfo",
  "jwks_uri":               "https://auth.tesla.com/oauth2/v3/discovery/thirdparty/keys",
  "scopes_supported":         ["email", "profile", "openid", "metadata"],
  "response_types_supported": ["code"],
  "grant_types_supported":    ["authorization_code"],
  "claims_supported":         ["iss", "iat", "exp", "nonce", "sub", "aud"]
}
```

Note: `/token` calls should target the `fleet-auth.prd.vn.cloud.tesla.com` domain
for application-server rate limits (the auth.tesla.com host also works for the
authorize redirect).

### Authorization code flow

1. Redirect the user to `/authorize`:

   ```
   https://auth.tesla.com/oauth2/v3/authorize
     ?client_id=$CLIENT_ID
     &redirect_uri=$REDIRECT_URI      # must match app config EXACTLY (scheme+host+path)
     &response_type=code
     &scope=openid email profile offline_access
     &state=$STATE
   ```

2. Exchange the returned `code`:

   ```
   POST https://auth.tesla.com/oauth2/v3/token
   grant_type=authorization_code
   client_id=$CLIENT_ID
   client_secret=$CLIENT_SECRET
   code=$CODE
   redirect_uri=$REDIRECT_URI
   ```

3. Fetch identity for account creation:

   ```
   GET https://auth.tesla.com/oauth2/v3/userinfo
   Authorization: Bearer <access_token>
   ```

   Use the `sub` claim as the stable primary key; `email`/`profile` populate the
   account.

### Refresh tokens

- Requires the `offline_access` scope.
- **Rotating**: each refresh returns a NEW refresh token — persist it and use it
  next time. Reusing an old one eventually triggers 401/403.
- Lifetime ~3 months; single-use. Needs per-user locking to avoid concurrent
  refreshes racing.

### Registration requirements (not a public client)

Unlike the `ownerapi` public client, Fleet API requires:

- A registered third-party app → `client_id` + `client_secret`.
- A **public key hosted at a `.well-known` path on your product's domain**.
- A partner-token registration call.
- Exact redirect-URI match (the #1 failure mode; no trailing-slash drift).

---

## 2. `ownerapi` first-party client (what dashcam.tesla.com uses)

The encrypted-clip decrypt endpoint is a **first-party** surface, not part of the
documented Fleet API. The community-standard way to obtain a token it accepts is
the legacy `ownerapi` public client (no app registration, PKCE, browser login):

- authorize: `https://auth.tesla.com/oauth2/v3/authorize`
- token:     `https://auth.tesla.com/oauth2/v3/token`
- `client_id=ownerapi`
- `redirect_uri=https://auth.tesla.com/void/callback` (blank page; user pastes URL back)
- `scope=openid email offline_access`, PKCE S256

Implemented in `internal/teslaauth`.

### Decrypt endpoint

```
POST https://dashcam.tesla.com/api/1/decrypt/batch
Authorization: Bearer <token>
{ "items": [ { id, vin, key_id, timestamp, wrapped_key, public_key } ] }
→ { "results": [ { id, key(base64 AES-128), error } ] }
```

Only file metadata is sent; video bytes stay local. See
`third_party/tesla-dashcam-decrypt/WRITEUP.md` for the full scheme and
`internal/dashcam` for the Go port.

### Open question (being spiked)

Whether a **Fleet API** third-party access token is accepted by
`/api/1/decrypt/batch`. If yes, one token covers both login and decryption. If it
401s (likely, due to audience mismatch), the two surfaces stay separate:
Fleet OIDC for login, reverse-engineered `ownerapi` token for decryption.

---

## Sources

- Fleet API auth overview — https://developer.tesla.com/docs/fleet-api/authentication/overview
- Third-party tokens — https://developer.tesla.com/docs/fleet-api/authentication/third-party-tokens
- OIDC discovery — https://fleet-auth.prd.vn.cloud.tesla.com/oauth2/v3/thirdparty/.well-known/openid-configuration
