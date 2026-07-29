# Version / Meta API — Design Spec

**Date:** 2026-07-29
**Status:** Approved design, pre-implementation

## Goal

Expose backend version information over a typed, publicly-accessible API so clients
can display:

- The **API (backend) version** and its **git commit hash**.
- The **voice server version(s)** and their git commit hashes, read **live** from the
  servers currently registered with the API (not a compile-time guess).

Access is **unauthenticated** ("allow all access") — no token required, not rate-limited.

---

## Client Contract

### Method

Clients call a single RPC on the new **`MetaService`**:

| | |
|---|---|
| gRPC method | `concord.meta.v1.MetaService/GetVersion` |
| REST (grpc-gateway) | `GET /v1/version` |
| Auth | **None.** Public method; no `Authorization` header needed. |
| Rate limit | Exempt. |

The legacy ad-hoc `GET /version` JSON endpoint is **retired** and replaced by
`GET /v1/version`.

### Protobuf contract

`api/proto/meta/v1/meta.proto`:

```proto
syntax = "proto3";

package concord.meta.v1;

option go_package = "github.com/Alexander-D-Karpov/concord/api/gen/go/meta/v1;metav1";

import "google/api/annotations.proto";

// A single build's version identity.
message ComponentVersion {
  string version  = 1; // full string, e.g. "F-WTSS-0.3.0"
  string codename = 2; // e.g. "F-WTSS"
  int32  major    = 3;
  int32  minor    = 4;
  int32  patch    = 5;
  string commit   = 6; // short git hash, e.g. "7c8870d"; "unknown" if not baked in
}

// Version identity of one registered voice server.
message VoiceServerVersion {
  string id      = 1;
  string name    = 2;
  string region  = 3;
  string status  = 4; // "online"
  string version = 5; // e.g. "0.2.0"
  string commit  = 6; // short git hash; "unknown" if not baked in
}

message GetVersionRequest {}

message GetVersionResponse {
  ComponentVersion            api           = 1;
  repeated VoiceServerVersion voice_servers = 2; // live, from registry; may be empty
}

service MetaService {
  rpc GetVersion(GetVersionRequest) returns (GetVersionResponse) {
    option (google.api.http) = { get: "/v1/version" };
  }
}
```

### Example response (`GET /v1/version`)

```json
{
  "api": {
    "version": "F-WTSS-0.3.0",
    "codename": "F-WTSS",
    "major": 0,
    "minor": 3,
    "patch": 0,
    "commit": "7c8870d"
  },
  "voiceServers": [
    {
      "id": "3f2a...",
      "name": "voice-eu-1",
      "region": "eu",
      "status": "online",
      "version": "0.2.0",
      "commit": "7c8870d"
    }
  ]
}
```

### What the client should display

- **Backend version line:** `api.version` (e.g. `F-WTSS-0.3.0`), with `api.commit`
  shown as a short hash — ideal as a small footer / "About" entry, e.g.
  `Backend F-WTSS-0.3.0 (7c8870d)`.
- **Voice server(s):** iterate `voice_servers`. For each, show `name` (or `region`)
  and `version (commit)`, e.g. `voice-eu-1 — 0.2.0 (7c8870d)`.
- **Empty `voice_servers`:** display "No voice servers online" — this is a valid state
  (no node registered, or all just restarted). Not an error.
- **`commit == "unknown"`:** a dev/unstamped build. Client may hide the hash or show
  `(dev)`. Not an error.
- **Field growth:** treat unknown future fields as optional; the contract only adds,
  never renames, within `v1`.

### Client call sketch (Go gRPC)

```go
resp, err := metav1.NewMetaServiceClient(conn).
    GetVersion(ctx, &metav1.GetVersionRequest{})
// resp.Api.Version, resp.Api.Commit, resp.VoiceServers[i].Version, ...
```

Or REST: `GET http://<host>:8080/v1/version` — no headers required.

---

## Server-side Implementation

### 1. Git commit injection (both binaries)

`internal/version/version.go`:

```go
// Commit is the short git hash, injected at build time via -ldflags.
// Defaults to "unknown" for `go run` / unstamped dev builds.
var Commit = "unknown"

func CommitHash() string { return Commit }
```

- **Makefile** `build`, `run-api`, `run-voice`: add
  `-ldflags "-X github.com/Alexander-D-Karpov/concord/internal/version.Commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)"`.
- **`deploy/Dockerfile.api` & `deploy/Dockerfile.voice`:** add `ARG GIT_COMMIT=unknown`
  before the build stage's `go build`, and pass it into the same `-ldflags -X`.
  (Chosen over `git rev-parse` inside the container: robust even if `.git` is absent
  from the build context and doesn't depend on git state inside the image.)
- **`deploy/docker-compose.yml`:** each service `build:` block gets
  `args: { GIT_COMMIT: "${GIT_COMMIT:-unknown}" }`. `make rebuild`/`update` export
  `GIT_COMMIT=$(git rev-parse --short HEAD)`.

### 2. Live voice version via registry

- **`api/proto/common/v1/types.proto`** — extend `VoiceServer`:
  ```proto
  string version = 10;
  string commit   = 11;
  ```
- **`internal/voice/discovery/registrar.go`** — populate `Version: version.Voice()`
  and `Commit: version.Commit` in `RegisterServerRequest`; drop the `%s/v%s` name hack
  so version lives in exactly one place.
- **Migration `015_voice_server_version.sql`:**
  ```sql
  ALTER TABLE voice_servers ADD COLUMN IF NOT EXISTS version TEXT;
  ALTER TABLE voice_servers ADD COLUMN IF NOT EXISTS commit  TEXT;
  ```
- **`internal/registry/repository.go`** — coordinated edits (all four or it panics):
  1. `VoiceServer` struct: add `Version string`, `Commit string`.
  2. `Upsert`: add columns to INSERT + `ON CONFLICT` UPDATE.
  3. `List`: add columns to the SELECT.
  4. `List`: add the matching fields to `rows.Scan`.
- **`internal/registry/handler.go`** — map the two new fields both directions
  (proto ↔ domain) in RegisterServer and ListServers.

### 3. Meta handler

`internal/meta/handler.go` — implements `metav1.MetaServiceServer`:

- `api` from the `version` package (`version.API()`, `APICodename()`, `APIMajor/Minor/Patch`, `CommitHash()`).
- `voice_servers` from `registry.Service.ListServers(ctx, nil)`, mapped to
  `VoiceServerVersion`. `List` filters `status='online'`, so a just-restarted node is
  briefly absent — acceptable for a version page.

### 4. Wiring & access (in `cmd/concord-api/main.go`)

- Register `metav1.RegisterMetaServiceServer(grpcServer, metaHandler)`.
- Register gateway handler `metav1.RegisterMetaServiceHandler(...)`.
- Remove the legacy `httpMux.HandleFunc("/version", …)` block.
- **Auth allowlist** (`internal/auth/interceptor/interceptor.go`): add
  `"/concord.meta.v1.MetaService/GetVersion": true` to `publicMethods`.
- **Rate-limit exemption** (`internal/ratelimit/interceptor.go`): add
  `"/concord.meta.v1."` to `exemptPrefixes`.

### 5. Proto generation

Run `make proto` (adds `api/gen/go/meta/v1/*` and regenerates common types).

---

## Verification

1. `make proto && make build` — no errors; generated `meta/v1` present.
2. Run the stack with a registered voice node; `curl -s localhost:8080/v1/version | jq`:
   - `api.commit` is a real short hash (not `unknown`) when built via Makefile/Docker.
   - `voice_servers[]` lists the node with its `version` and `commit`.
3. `curl` without an `Authorization` header succeeds (public access confirmed).
4. `go test ./internal/voice/discovery/...` — registrar metadata-keys test still passes.
5. `go test ./...` — registry repo/handler round-trip intact.

## Out of scope

- Historical/version-diff tracking.
- Reporting offline voice servers.
- Per-request auth or scoping (deliberately public).
```

