# Concord OAuth — Client Contract

How a client authenticates a user with an OAuth provider (Google today) against the
Concord API. The server owns the client secret **and** the PKCE verifier; the
client only opens a URL and catches a redirect.

## Conventions

- **Base URL:** the API HTTP/JSON gateway (e.g. `https://concord.akarpov.ru`).
- **JSON is camelCase**, both directions. Send `redirectUri`, not `redirect_uri`
  (unknown fields are silently dropped, so a snake_case body reads as empty).
- **Auth header:** authenticated calls send `Authorization: Bearer <accessToken>`.
  The auth-bootstrap endpoints below are public.
- **Errors:** non-2xx returns `{"code":<grpcCode>,"message":"...","details":[]}`.
  Common mappings: `400` invalid argument, `401` unauthenticated (bad/expired/
  replayed `state`, rejected `code`), `403`, `409`, `500`.
- **Golden rule:** open `authUrl` in the **system default browser**, never an
  embedded webview/Chromium. That reuses the user's existing Google session and is
  required by Google for native apps.

## Endpoints

| # | Method & path | Auth | Purpose |
|---|---|---|---|
| 1 | `GET /v1/auth/methods` | public | List available auth methods |
| 2 | `POST /v1/auth/oauth/begin` | public | Start login → `authUrl` + `state` |
| 3 | `POST /v1/auth/oauth` | public | Complete: `code` + `state` → tokens |
| 4 | `POST /v1/auth/refresh` | public | Rotate access token |
| 5 | `POST /v1/auth/logout` | public | Revoke a refresh token |

Password auth (`POST /v1/auth/login`, `POST /v1/auth/register`) is unchanged.

## Canonical flow

### 1. Discover methods

`GET /v1/auth/methods` → `200`

```json
{
  "methods": [
    { "id": "password", "type": "password", "displayName": "Password", "icon": "", "beginPath": "" },
    { "id": "google",   "type": "oauth",    "displayName": "Google",   "icon": "google", "beginPath": "/v1/auth/oauth/begin" }
  ]
}
```

Render a button per method. `type:"password"` → your username/password form.
`type:"oauth"` → the flow below, calling `beginPath` with `id` as `provider`. A
provider appears **only if** it is configured server-side **and** passed startup
validation, so the client needs no per-provider special-casing.

### 2. Begin

`POST /v1/auth/oauth/begin`

```json
{ "provider": "google", "redirectUri": "http://127.0.0.1:54321/callback" }
```

→ `200`

```json
{ "authUrl": "https://accounts.google.com/o/oauth2/auth?...&code_challenge=...&code_challenge_method=S256&state=...", "state": "n0pQ...-Xy" }
```

**Store `state`.** The server bound it to `{provider, redirectUri, PKCE verifier}`
in Redis (10-minute TTL, single use). The client handles no PKCE.

### 3. Open `authUrl` in the system browser

Web: full-page redirect or popup. Desktop/mobile: system browser / Custom Tab /
`ASWebAuthenticationSession`. **Not** an embedded window.

### 4. Receive the callback

Provider redirects to your `redirectUri`:

```
http://127.0.0.1:54321/callback?code=4/0Ab...&state=n0pQ...-Xy
```

**Verify the returned `state` equals the value you stored in step 2.** If it does
not match, abort — do not call step 5. If the URL carries `?error=access_denied`,
the user cancelled; return to the login screen.

### 5. Exchange for tokens

`POST /v1/auth/oauth`

```json
{ "provider": "google", "code": "4/0Ab...", "state": "n0pQ...-Xy", "redirectUri": "http://127.0.0.1:54321/callback" }
```

→ `200`

```json
{ "accessToken": "eyJhbGc...", "expiresIn": 900, "refreshToken": "eyJhbGc...", "tokenType": "Bearer" }
```

The server consumes the `state` (one-time), exchanges the code with the provider
(secret + PKCE verifier stay server-side), finds-or-creates the account (a new
account gets a unique handle derived from the profile, and its avatar is ingested
from the provider picture into Concord's own storage), and returns the token pair.
`401` = expired/replayed `state` or a rejected code; `400` = missing fields or
provider unavailable.

### 6. Use, refresh, revoke

- Authenticated requests: `Authorization: Bearer <accessToken>`.
- On `401` (access token older than `expiresIn` seconds): `POST /v1/auth/refresh`
  with `{ "refreshToken": "..." }` → new `Token`; persist the rotated refresh token.
- Logout: `POST /v1/auth/logout` with `{ "refreshToken": "..." }` → `{}`; also clear
  local tokens.

## Desktop app recipe (system browser + loopback)

```
1. bind local server:  srv = listen("127.0.0.1:0")   → port = srv.port
2. begin(redirectUri = "http://127.0.0.1:{port}/callback")   → authUrl, state
3. open authUrl in the SYSTEM browser   (Linux: xdg-open; Electron: shell.openExternal)
4. srv receives GET /callback?code=&state=
      → assert state == stored state
      → respond 200 "You're signed in — you can close this tab."
      → close srv
5. exchange(provider, code, state, redirectUri)   → tokens
6. store tokens in the OS keychain/secret store; the app window takes over
```

Do **not** render the provider page in a `BrowserWindow`/webview.

## Redirect mechanisms (server allowlist)

| Client | `redirectUri` | Server validation |
|---|---|---|
| Desktop / Electron / Tauri / CLI | `http://127.0.0.1:<anyport>/callback` (or `localhost`) | loopback host + any port, always allowed |
| Mobile | `com.example.concord:/oauth2redirect` | exact match vs configured allowlist |
| Web SPA | `https://app.example.com/oauth/callback` | exact match vs configured allowlist |

- Google Cloud console: register `http://127.0.0.1` once for loopback; register each
  custom-scheme / web URI explicitly.
- Server config `OAUTH_GOOGLE_REDIRECT_URL` is the comma-separated exact-match
  allowlist; loopback is permitted by rule in addition.

## Client responsibilities (checklist)

1. `GET /v1/auth/methods` and render buttons dynamically.
2. `begin`, then **persist `state`**.
3. Open `authUrl` in the **system browser**; capture `code` + `state` at the redirect.
4. **Reject on `state` mismatch**; otherwise call `exchange`.
5. Store tokens securely; attach the bearer header; refresh on `401`; logout to revoke.

*(No nonce, no PKCE verifier, no `code_challenge` on the client — the server
generates and validates all of that.)*

## curl walkthrough

```bash
BASE=https://concord.akarpov.ru
RURI=http://127.0.0.1:54321/callback

# 1. what's available?
curl -s $BASE/v1/auth/methods | jq

# 2. begin -> authUrl + state
resp=$(curl -s -X POST $BASE/v1/auth/oauth/begin \
  -H 'Content-Type: application/json' \
  -d "{\"provider\":\"google\",\"redirectUri\":\"$RURI\"}")
echo "$resp" | jq -r .authUrl     # open this in a browser
STATE=$(echo "$resp" | jq -r .state)

# 3. after the redirect back with ?code=...&state=..., verify state==$STATE, then:
curl -s -X POST $BASE/v1/auth/oauth \
  -H 'Content-Type: application/json' \
  -d "{\"provider\":\"google\",\"code\":\"<CODE>\",\"state\":\"$STATE\",\"redirectUri\":\"$RURI\"}" | jq

# 4. use it
curl -s $BASE/v1/users/me -H "Authorization: Bearer <accessToken>" | jq
```

## Server configuration

Set for each provider (see `.env.example`):

```
OAUTH_GOOGLE_CLIENT_ID=...
OAUTH_GOOGLE_CLIENT_SECRET=...
OAUTH_GOOGLE_REDIRECT_URL=https://app.example.com/oauth/callback   # comma-separated allowlist
```

OAuth login also requires Redis (it stores per-request PKCE state). Adding a new
provider is a one-line entry in `oauth.Registry` (`internal/auth/oauth`) plus its
`OAUTH_<NAME>_*` env vars.
