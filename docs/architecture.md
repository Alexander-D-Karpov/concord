# Concord Architecture & Package Map

A navigation guide to the codebase. Read `CLAUDE.md` first for commands and conventions; this
document is the map you consult to find *which package owns a concern* before grepping.

Module: `github.com/Alexander-D-Karpov/concord`. Go 1.25.1. Postgres (pgx/pgxpool) + Redis.
IDs are 64-bit Snowflakes. Real-time fan-out goes through one in-process event Hub.

---

## 1. The two services (plus a CLI)

### `concord-api` (`cmd/concord-api`)
The stateful backend. Composition root is `cmd/concord-api/main.go:run()` — every repository,
service, and handler is constructed and injected there (constructor injection, no DI framework),
then registered on one gRPC server. It also fronts:

- an **HTTP/JSON gateway** (grpc-gateway) on `:8080`, with Swagger UI at `/docs` and file serving
  at `/files/`,
- **Prometheus metrics** (`:9100`) and **health** (`:8081`),
- background loops: typing-indicator cleanup, feature scheduler, poll auto-closer, voice-server
  health checker.

Owns PostgreSQL (migrations run on startup) and Redis (optional; degrades to no-cache if absent).

### `concord-voice` (`cmd/concord-voice`)
The stateless media server — a UDP SFU (Selective Forwarding Unit). Composition root is
`cmd/concord-voice/main.go:run()`. It holds **no database**. It relays encrypted audio/video between
peers in a room, and:

- registers itself with `concord-api` over the `registry` gRPC service and heartbeats every 30s
  (`internal/voice/discovery`),
- exposes metrics (`:9101`), health (`:8082`), a JSON status API, and an optional TCP/TLS fallback
  transport for clients whose UDP is blocked.

The voice server's live session state is **in-memory** and lost on restart (see
`internal/voice/session`). On the API side, `internal/voiceassign`'s per-user session membership is
also in-memory, though its room→server pin (Postgres) and room→crypto suite (Redis) are durable.

### `concord-cli` (`cmd/concord-cli`)
Django-`manage.py`-style operator tool (cobra). Connects **directly to Postgres/Redis** using the same
`.env`/config, so it works while the server runs; mutating commands bypass the API's gRPC auth
(operator = superuser). Commands: `dbshell`/`cacheshell` (exec psql/redis-cli), `migrate`/`migrate
status`, `user create|set-password|unlock|set-role`, `ban`/`unban`/`list-bans`, `settings get|set`
(JSON merge onto current), `stats`, `health`, `voice-servers`, `purge-messages`, `clear-ratelimit`.
Reuses `rooms.Repository`, `retention`, `audit`, `migrations`.

### How the two services connect
```
client ──gRPC──> concord-api ──assigns──> voice server addr + short-lived voice JWT
   │                  ▲  (registry gRPC: register + 30s heartbeat)
   │                  │
   └────UDP (encrypted media)────> concord-voice ──SFU relay──> other clients
```
The API's `internal/voiceassign` picks a voice server (region-aware load balancing), issues a voice
JWT, and tracks the live room→server / room→port / room→crypto mapping in memory.

---

## 2. The layering convention

Most domain packages under `internal/` follow a three-file split:

| File | Responsibility |
|------|----------------|
| `repository.go` | All SQL via the pgx pool; returns domain structs. |
| `service.go` | Business logic, authorization decisions, event emission. No gRPC types. |
| `handler.go` / `handlers.go` | gRPC service impl; translates proto ⇄ domain, reads identity from context. |

**Caching** comes in two flavors: a `NewRepositoryWithCache(...)` variant (only in `rooms`, `users`,
`social/friends`) or service-layer caching via `cache.AsidePattern` (chat, membership, slowmode,
polls) / raw `cache.Cache` (linkpreview, voiceassign).

Packages that deviate: `membership` (no repo — persists through `rooms.Repository`), `call` (thin
handler over `voiceassign`, no repo/service), several `messaging/*` sub-packages (utility
recorders/parsers, no handler).

---

## 3. Package map — API / platform

### Auth & security
- **`internal/auth`** — registration, password/OAuth login, token issue/refresh/revoke, single-use
  refresh-token rotation. Refresh tokens stored only as SHA-256 hashes. Password logins are throttled
  by a cache-backed `LockoutManager` (config `LOGIN_MAX_ATTEMPTS` / `LOGIN_LOCKOUT_PERIOD` /
  `LOGIN_ATTEMPT_WINDOW`); a locked identifier is rejected with a too-many-requests error.
- **`internal/auth/jwt`** — `Manager` mints/validates three token classes (access, refresh, voice).
  Voice tokens use a **separate secret**; `validateToken` enforces the expected type so tokens can't
  be replayed across classes.
- **`internal/auth/oauth`** — OAuth2 (Google, GitHub): auth URLs, code exchange, per-provider
  userinfo normalization, CSRF `state`.
- **`internal/auth/interceptor`** — gRPC unary+stream auth interceptors; inject user identity into
  context. Two allowlists: `publicMethods` (skip auth — login/register/refresh/reflection/health)
  and `machineAuthMethods`. **Any new public RPC must be added to `publicMethods` or it 401s.**
- **`internal/authz`** — in-memory RBAC (member/moderator/admin) + cache-backed permission checks.
  Role assignments are **not persisted** (in-memory only). `InvalidateUser` drops a user's cached
  decisions via a SCAN-based pattern delete; otherwise a decision clears on TTL expiry. (The package
  has no production callers yet.)
- **`internal/security`** — TLS config helpers. `LoadTLSConfig` = mTLS with CA pool; `ServerTLSConfig`
  = server cert only. Pick by trust direction.
- **`internal/registry`** — voice-server registry: servers register/heartbeat, are ranked by
  `calculateLoadScore`, listed for assignment. Machine-to-machine RPCs auth via
  `MachineAuthInterceptor` (server secrets stored hashed).

### Cross-cutting middleware
- **`internal/middleware`** — recovery, request logging, per-method timeout overrides
  (`longTimeoutMethods`), validation, HTTP gzip. `quietMethodPrefixes` suppress noisy logs.
- **`internal/ratelimit`** — distributed token-bucket via a Redis Lua script with in-memory
  fallback, per-category (auth/message/upload/read/…). `NewLimiter` starts a cleanup goroutine —
  **call `Close()`**. Bypass token only honored in voice-debug mode.
- **`internal/gateway`** — grpc-gateway HTTP/JSON proxy with CORS/logging/version headers. `Init(ctx)`
  must run before `Start`.
- **`internal/swagger`** — serves embedded Swagger UI + OpenAPI spec; loads spec at construction.
- **`internal/circuitbreaker`** — 3-state breaker. `Call` holds the lock for the whole `fn` — calls
  through one breaker are **serialized**; don't share across high-concurrency independent calls.
- **`internal/retry`** — exponential backoff with jitter. Retries on **any** error (no retryable
  predicate) — don't wrap non-idempotent work.
- **`internal/observability`** — health checker + Prometheus metrics (own HTTP servers) +
  request-ID/correlation-ID interceptors.
- **`internal/version`** — version constants and Concorde-themed codenames for API and Voice.

### Infrastructure
- **`internal/infra`** — `SnowflakeGenerator` (custom epoch 2022-01-01; 41-bit ms | 10-bit worker |
  12-bit seq; lock-serialized).
- **`internal/infra/db`** — pgxpool wrapper with retry (`WithRetry`/`isRetriable`), pool monitor,
  slow-query tracer.
- **`internal/infra/cache`** — Redis wrapper + `AsidePattern`. **`GetOrLoad` does not dedupe
  concurrent loads** (thundering herd) and swallows `Set` errors. Miss sentinel `ErrCacheMiss` —
  compare with `errors.Is`.
- **`internal/infra/migrations`** — embedded SQL runner. Files `NNN_*.sql` applied in ascending order
  on API startup, tracked in a migrations table. Schema source of truth.
- **`internal/common/config`** — typed config from **plain `os.Getenv`** (no viper/tags). Groups:
  Server, Database, Auth, Voice, Logging, Redis, RateLimit, Storage, Email. `Voice.Debug`
  (`VOICE_DEBUG`) is the production-forbidden stress-test switch.
- **`internal/common/logging`** — zap init + custom `TraceLevel`, HTTP level control, and a TTY-only
  sticky status line (`SetStatus`/`StatusEnabled`). Global mutable logger state.
- **`internal/common/errors`** — `AppError` implements `GRPCStatus()`, so returning it from a handler
  yields the right gRPC code. Constructors: `NotFound`/`Unauthorized`/`Forbidden`/`BadRequest`/
  `Conflict`/`Internal`.
- **`internal/common/netinfo`** — computes the advertised host (config > env > public-IP probe > LAN
  > loopback) and prints the startup banner. **Makes outbound network calls at startup.**
- **`internal/storage`** — local-filesystem blob storage + HTTP serving; decodes image dimensions
  (PNG/JPEG/GIF/WebP) from the header on store.
- **`internal/testutil`** — DB-backed test helpers (pool, truncate, seed). Needs a live Postgres.

### Real-time
- **`internal/events`** — the **Hub**: in-process pub/sub broadcasting `ServerEvent`s to connected
  gRPC event streams. `clients[userID]` + `rooms[roomID][userID]`, each client has a `writePump`.
  Broadcasts are best-effort (full channel drops the event). `AddClient` returns nil during shutdown.
  **Injected nearly everywhere; the single fan-out point — services emit events, never push to
  clients directly.**
- **`internal/stream`** — `StreamService.EventStream` handler wiring a client stream into the Hub.
  `VoiceSnapshotSender` is injected post-construction to break an import cycle with voice.

---

## 4. Package map — domain features

- **`internal/users`** (+ `presence.go`) — profiles, handles, OAuth lookup, avatars (image pipeline),
  status, batch lookups. `PresenceManager` keeps presence in an **in-memory map** (per-process, lost
  on restart); status changes broadcast — only when the effective status actually changes — to the
  user's friends and their shared rooms.
- **`internal/rooms`** — room CRUD, membership storage (members/roles/nicknames), invites,
  voice-server attachment. **Owns the room, membership, and room-invite tables** (role and nickname
  are columns on the membership table, not separate tables) even though `membership` is separate. Has
  a cache variant.
- **`internal/membership`** — invite/accept/reject/remove/set-role/nickname logic. No repo — persists
  through `rooms.Repository`. Calls an injected `KeyRotator` only on member **removal** (rotating the
  room's voice key so the removed member loses access); invite/accept/reject/set-role/nickname do not.
- **`internal/chat`** — room text messaging: send/edit/delete, reactions, pins, threads, search,
  mentions. Files use `handlers.go` (plural). `SendMessage` enforces slow-mode and fires mention
  notifications as a side effect.
- **`internal/dm`** — direct-message channels + DM voice calls. **Two repositories**: `Repository`
  (channels/calls) and `MessageRepository` (messages). Read-tracking/typing services are
  setter-injected and nil-guarded.
- **`internal/social/friends`** — friend requests, friendships, blocks. Friendships stored as ordered
  pairs (cache invalidated both directions). Blocking a user triggers shared-room voice-key rotation
  so the blocked user loses access.
- **`internal/call`** — gRPC voice-call control plane for rooms (join/leave/media-prefs/status) +
  `Snapshotter` that pushes full voice state to a reconnecting user. Thin layer over `voiceassign`.
  Three access tiers: `requireVoiceAccess` / `requireAuthed` / `requireMember`.
- **`internal/voiceassign`** — assigns users to voice servers (region-aware), issues voice JWTs,
  tracks live sessions/crypto/ports, health-checks servers. Durability is mixed: the **room→server
  pin is persisted to Postgres** and the **room→crypto suite is cached in Redis (24h)**, so both
  survive a restart; **only the per-user session membership is in-memory** and lost on restart.
  `StartHealthChecker` is a blocking loop (main runs it in a goroutine) that marks lapsed servers
  offline, evicts their sessions, and notifies affected users to rejoin — nothing is re-homed in
  place; reassignment happens when those users reconnect.
- **`internal/admin`** — moderation (kick/ban/unban/mute, list bans/mutes/audit-log) plus per-room
  settings (`GetRoomSettings`/`UpdateRoomSettings`); re-checks role from DB before acting, broadcasts
  via Hub, and writes audit records. Ban/mute/settings storage lives in `rooms.Repository`, so **bans
  are enforced** at `membership.AcceptRoomInvite` and the voice-join gate (`call.requireMember`), and
  settings drive enforcement in membership (who-can-invite, atomic member-cap), chat (who-can-post,
  word filter on send + edit), and the retention purger. Kick/ban also **isolate the target from live
  voice** via an injected `VoiceEvictor` — it clears their session placement then rotates the room key
  so remaining members re-key and the evicted user can no longer decrypt or be decrypted (it does not
  force-close their UDP socket; that would need a voice-server control RPC). Content toggles
  (link/gif/sticker) and require-approval are advisory (stored, client-honored).
- **`internal/retention`** — background purger (`RunPurger`) that soft-deletes messages older than a
  room's `retention_days` setting; wired into concord-api's background loops.
- **`internal/audit`** — records moderation events to the `audit_log` table (`Log`/`List`, with
  `LogKick`/`LogBan` helpers); IDs are stored as TEXT so history survives room/user deletion.
- **`internal/features`** (+ `gifprovider`) — aggregate "extra messaging" service: forwards,
  scheduled messages, bookmarks, drafts, notification overrides, stickers, GIF search. `RunScheduler`
  is a background delivery loop using a `FOR UPDATE`-style claim. `Aggregator` composes newer
  per-feature services (polls, slowmode) over the legacy monolithic `Service`. GIF search is disabled
  without `GIF_API_KEY`.

### `internal/messaging/*` — shared message machinery
- **`messaging`** (top-level) — surface-agnostic unified `Message` type + `Surface` enum
  (Room/DM/Unknown) + proto mappers. Distinct from `chat.Message` and `dm.DMMessage`; use
  `SurfaceID()` rather than reading `RoomID`/`ChannelID` directly.
- **`messaging/editing`** — edit-history recorder/reader. `Recorder` methods require a caller-supplied
  `pgx.Tx` (must run inside the message-update tx). Room and DM edits in separate tables.
- **`messaging/mentions`** — parses `@`-mentions to user IDs, merging client hints with DB validation.
- **`messaging/polls`** — poll create/vote/close (+ auto-close loop `RunCloser`); voting is a
  multi-step tx with denormalized recount. Inserts a backing message via a `messageInserter` seam.
- **`messaging/readtracking`** — last-read + unread counts for rooms/DMs (parallel method sets);
  last-read stored as int64 snowflake, compared numerically. `Mark*AsRead` broadcasts.
- **`messaging/slowmode`** — per-room slow-mode. `CheckAndStamp` both checks the cooldown **and**
  records the send — calling it twice double-stamps. `exemptRole` bypasses.
- **`messaging/typing`** — ephemeral typing indicators (rooms/DMs) with DB expiry + an in-memory
  per-(user, target) rate limiter (2s). `CleanupExpired` must run periodically (API does, every 2s).
- **`messaging/linkpreview`** — URL unfurling (OpenGraph); package name is `unfurl`. SSRF-hardened:
  a custom `DialContext` enforces a port allow-list and rejects loopback/link-local/private/multicast/
  unspecified IPs, the `169.254.169.254` metadata address, and IPv6 unique-local, and limits redirects.
  Body capped by `io.LimitReader`; results cached by raw URL.

---

## 5. Package map — voice subsystem (`internal/voice/*`)

Live path is **`udp.ServerPool` + `session.Manager` + `congestion.Controller` + `router.Router`**.
Startup wiring order (`cmd/concord-voice/main.go`): `session.NewManager` → `congestion.NewController`
→ `router.NewRouter` → `udp.NewServerPool` → `router.SetDefaultConn(pool.PrimaryConn())` → control /
discovery / status / health / telemetry servers; a 1s ticker calls `StepTiers`, `SweepInactive`,
`Prune`.

- **`voice/udp`** — the data plane. `ServerPool` (production, multi-socket SO_REUSEPORT or per-port);
  `Server` (legacy single-socket, built `ctrl=nil` so congestion is **disabled** on that path);
  `Handler` (packet dispatcher on `data[0]`). Uses **refcounted pooled buffers** (`Retain`/`Release`)
  — a missed Release leaks, a double Release corrupts the pool. `tryMigrateByMedia` rebinds a
  session's address only after a **decrypt-verify** (proves key possession) + 1s cooldown —
  security-critical.
- **`voice/router`** — SFU forwarding. N workers (`NumCPU*2`, 4..32), each with control>audio>video
  queues; SSRC hashes to a worker (single-threaded per destination). Media older than 80ms is dropped
  at send. `SetDefaultConn` is the fallback egress for TCP-origin media.
- **`voice/session`** — authoritative session registry. `Manager` (5 index maps under one RWMutex) +
  `Session` (per-participant, own `Mu`). Two-stage inactivity: idle→inactive (marked once) →
  idle→removed, preserving SSRC/crypto in between so resume keeps identity. `GetRoomSessions` returns
  an atomic snapshot. `SetCrypto` keeps `prevCrypto` for key-rotation overlap.
- **`voice/congestion`** — pure, lock-guarded congestion state machine (RR aggregation, per-stream
  bitrate targets, PLI rate-limits, simulcast tier ceilings). No I/O; every time-dependent method
  takes an explicit `now`. Nil-safe everywhere. **Simulcast layer dropping is intentionally inert on
  single-SSRC streams** — don't enable until clients ship per-layer SSRCs.
- **`voice/crypto`** — AES-256-GCM with **per-SSRC HKDF-derived nonce bases** (prevents cross-sender
  nonce reuse; server derivation must match client byte-for-byte) + a sliding-window replay filter.
  `DecryptSSRC` runs the replay check **before** decrypt — load-bearing for migration safety.
- **`voice/protocol`** — on-the-wire format. `ProtocolVersion=3`; 24-byte media header (incl. `Layer`
  byte), fragment headers, JSON control payloads (Hello/Welcome/Nack/Pli/RR/QualityPref/BitrateHint…).
  Protocol is negotiated down to what the client speaks (a strict v2 client re-Hellos forever if sent
  an unsolicited v3 Welcome).
- **`voice/discovery`** — the **client** side of registration: dials the API's RegistryService,
  registers, runs the heartbeat loop, re-registers after 3 failures. Two secrets:
  `registerSecret` (register) vs `serverSecret` (heartbeat).
- **`voice/control`** — the voice node's **local** gRPC RegistryService server; `Heartbeat` is a
  near-stub. Don't confuse with `discovery` (outbound).
- **`voice/tcp`** — optional TCP/TLS fallback; frames carry the identical wire format through the same
  `udp.Handler`, keyed by a **synthetic UDPAddr** so they slot into the addr-indexed maps. Replies go
  over the stream (`session.Transport`), not the UDP socket.
- **`voice/status`** — authenticated JSON status API (rooms, room detail, stats). Uses
  `ValidateAccessToken` (not the voice token). CORS wide-open.
- **`voice/health`** — HTTP liveness/readiness aggregating named checks; any failure → 503.
- **`voice/telemetry`** — atomic-counter metrics, Prometheus/JSON exposition, interval snapshots, and
  CPU/egress `LoadSampler` (platform-split `load_linux.go`/`load_other.go`; first `Sample` returns 0).
- **`voice/room`** — ⚠️ **legacy/near-dead**: a secondary room index that is never populated with
  sessions; only `GetAllRooms()` is used (for a health check). The real index is `session.Manager`.

---

## 6. Key data flows

### Request authentication
Interceptor chain (`cmd/concord-api/main.go`): recovery → request-id → metrics → timeout →
machine-auth → **auth** → logging → rate-limit. The auth interceptor validates the JWT and injects
`userID`/`handle`/`claims` into the context; handlers read them via `interceptor.GetUserID(ctx)`.
Public RPCs are exempted via the `publicMethods` allowlist.

### A chat message reaching other clients
`chat.Handler.SendMessage` → `chat.Service` (slow-mode check, mention parse, persist via
`chat.Repository`, with edit-history recording inside the repository's core edit transaction) → emits
a `ServerEvent` to `events.Hub` →
`Hub.BroadcastToRoom` → each subscribed client's `writePump` → `stream.EventStream` → client.
Services never write to client streams directly.

### Voice media (client → peers)
1. Client sends UDP `Hello` (voice JWT + crypto material).
2. `ServerPool.readLoop` reads into a pooled buffer; a worker calls `Handler.HandlePacketOwned`.
3. `handleHello`: rate-gate per IP → validate voice token → `session.Manager.CreateSession`
   (assigns SSRCs + per-SSRC-derived crypto) → reply `Welcome` (negotiated protocol) + broadcast join.
4. Media packets: `handleMedia` finds the session by source addr (or decrypt-verified SSRC
   migration), validates SSRC ownership, `Touch`es activity, records loss/congestion signals, calls
   `router.RouteMediaOwned`.
5. `router` finds the room, iterates the snapshot, skips sender/unsubscribed peers, applies opt-in
   simulcast selection, enqueues a pooled `sendTask` (Retaining the buffer) on the destination
   worker's queue.
6. `sendWorker` drains control>audio>video, drops media >80ms old, writes to the peer's TCP transport
   if set else the UDP socket, then `Release`s the buffer.
7. Control feedback: NACK→retransmit buffer, PLI/RR→congestion controller + forward,
   QualityReport/Speaking/MediaState→room broadcast — all through the router's control lane.

### Voice server registration
On startup `discovery.Registrar.Register` calls the API's RegistryService (auth `x-voice-secret` =
registerSecret), advertising UDP/control addresses from `netinfo`. `StartHeartbeat` then sends 30s
heartbeats (auth serverSecret + server-id, stats from `telemetry.LoadSampler`), re-registering after
3 consecutive failures. Meanwhile `internal/registry` on the API side stores/ranks servers, and
`internal/voiceassign` picks one per room.

---

## 7. Legacy / cleanup flags for future sessions

- **`voice/room` package** and `session.RoomManager` are redundant with `session.Manager`'s room
  index — a refactor leftover, safe to treat as dead.
- **`udp.Server` (single-socket)** is the legacy path with congestion disabled (`ctrl=nil`); only
  `ServerPool` is live.
- The recent voice refactor **deleted** `voice/cdn`, `voice/qos`, and `voice/sync/frame_sync.go` and
  added `voice/congestion`. No dangling references to the deleted packages remain.
- Presence, voice-server sessions, and `voiceassign`'s per-user session membership are **in-memory** —
  a horizontal-scaling or restart-durability effort would start there. (Note `voiceassign`'s room→server
  pin and room→crypto suite are already durable in Postgres/Redis.)
