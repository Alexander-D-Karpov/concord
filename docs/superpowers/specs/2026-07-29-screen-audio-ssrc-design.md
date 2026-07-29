# Design: separate SSRC for desktop screen-share audio

**Date:** 2026-07-29
**Status:** approved (pending spec review)

## Goal

Give desktop screen-share audio its own SSRC (`screen_audio_ssrc`), a fourth SSRC
per session alongside mic (`ssrc`), camera video (`video_ssrc`), and screen video
(`screen_ssrc`). Screen audio is a distinct Opus stream: it must route independently
of the mic (surviving mic-mute), never drive speaking indicators, and be toggled by
its own `screen_audio_enabled` flag rather than being derived from `screen_sharing`
(a share can have no audio track — Linux/portal, or a window with no audio).

## The two-plane insight (shapes all scope)

The codebase treats SSRCs very differently in two places:

- **Data plane** (`internal/voice/**` — the voice server / SFU): SSRCs are **real**.
  Allocated per session, indexed in `Manager.ssrcMap`, and used to look up sessions
  for routing. The client receives its SSRCs in the UDP `WELCOME` JSON. **This is
  where the actual work is.**
- **Control plane** (`internal/call`, `internal/voiceassign`, `internal/dm`, gRPC
  protos): the SSRC proto fields (`callv1.Participant.screen_ssrc`,
  `streamv1.VoiceParticipantState.screen_ssrc`, `JoinVoiceResponse.screen_ssrc`)
  **exist but are never populated** — the control plane does not know SSRCs; the
  client learns them from the UDP `WELCOME`. Only the *enabled* flags
  (`muted` / `video_enabled` / `screen_sharing`) are genuinely tracked here.

Therefore:
- `screen_audio_ssrc` → **real routing work** on the data plane; **decorative field
  parity** on the control plane (added for spec-completeness and forward-compat,
  unpopulated exactly like `screen_ssrc` is today).
- `screen_audio_enabled` → **genuinely plumbed** everywhere `screen_sharing` is.

## Locked decisions

- **Channels: mono.** Client downmixes desktop audio at capture; zero negotiation.
  The `AudioDecoder` stays `numberOfChannels: 1`. No `channels` field on the wire.
  (Stereo is explicitly out of scope for this pass.)
- **No crypto change.** `crypto.SessionCrypto.DecryptSSRC(aad, ct, counter, ssrc)`
  already derives the nonce base per-SSRC via HKDF (`DeriveNonceBase` mixes the
  ssrc), so `screen_audio_ssrc` decrypts correctly with no change. The server only
  decrypts for address-migration verification (`tryMigrateByMedia`); its per-session
  `ReplayFilter` is shared across that session's SSRCs today (mic/video/screen) and
  is unaffected by adding a fourth. There is no per-SSRC replay filter to instantiate
  because the SFU forwards opaque ciphertext — it never decrypts the media path.
- **SSRC proto parity (decision B):** add `screen_audio_ssrc` everywhere
  `screen_ssrc` already appears in the control-plane protos, even though it stays
  unpopulated (matches spec, harmless, consistent).
- **GetVoiceStatus (decision A):** `callv1.VoiceParticipant` carries **no** SSRC
  fields today; add only `screen_audio_enabled` (parallel to its existing
  `screen_sharing`), no lone SSRC.

## Touchpoint checklist

Grouped by file. Each line is an implementation step; the test plan follows.

### Data plane — the substance

**`internal/voice/session/session.go`**
1. `Session` struct: add `ScreenAudioSSRC uint32` (after `ScreenSSRC`) and
   `ScreenAudioEnabled bool` (near `ScreenSharing`).
2. `CreateSession`: when `!observer`, allocate a fourth SSRC
   (`screenAudioSSRC = m.nextSSRC; m.nextSSRC++`), set it on the session, and add
   `m.ssrcMap[screenAudioSSRC] = sess`.
3. `RemoveSession`: `delete(m.ssrcMap, sess.ScreenAudioSSRC)`.
4. `SweepInactive`: `delete(m.ssrcMap, sess.ScreenAudioSSRC)` in the remove branch;
   add `ScreenAudioSSRC` to the `InactiveInfo` built in the inactive branch.
5. `InactiveInfo` struct: add `ScreenAudioSSRC uint32`.
6. Add `func (s *Session) SetScreenAudioEnabled(bool)` mirroring `SetScreenSharing`.

**`internal/voice/router/router.go` — the one real logic change**
7. `routeMedia` mute gate: change
   `if h.Type == protocol.PacketTypeAudio && sender.Muted` →
   `if h.Type == protocol.PacketTypeAudio && h.SSRC == sender.SSRC && sender.Muted`.
   `sender` resolves from `h.SSRC` via `ssrcMap`, so a screen-audio packet has
   `h.SSRC == sender.ScreenAudioSSRC` and correctly bypasses the mic-mute drop.
   No other cull applies to audio (subscription check is opaque; no top-N/VAD here).

**`internal/voice/udp/handler.go`**
8. `mediaSSRCMatchesSession` audio case:
   `return ssrc != 0 && (ssrc == sess.SSRC || ssrc == sess.ScreenAudioSSRC)`.
   (Also enables address-migration via a screen-audio packet, for free.)
9. `buildWelcome`: set `ScreenAudioSSRC: sess.ScreenAudioSSRC`.
10. `roomParticipants`: set `ScreenAudioSSRC` and `ScreenAudioEnabled` per participant.
11. `handleMediaState`: `sess.SetScreenAudioEnabled(ms.ScreenAudioEnabled)`.
12. `broadcastJoined`: set `ScreenAudioSSRC` + `ScreenAudioEnabled`.
13. `broadcastMediaState`: set `ScreenAudioSSRC` + `ScreenAudioEnabled`.
14. `broadcastParticipantLeft`: add `screenAudioSSRC uint32` param; set in payload.
15. `handleBye`: capture `screenAudioSSRC := sess.ScreenAudioSSRC` before remove; pass it.
16. `SweepAndNotify`: pass `info.ScreenAudioSSRC` to `broadcastParticipantLeft`.
17. (nice-to-have) `handleHello` "session created" log: add `ssrc_screen_audio`.

**`internal/voice/protocol/protocol.go`** (`omitempty` matches the sibling SSRC fields)
18. `WelcomePayload`: `ScreenAudioSSRC uint32 \`json:"screen_audio_ssrc,omitempty"\``.
19. `ParticipantInfo`: `ScreenAudioSSRC uint32 \`json:"screen_audio_ssrc,omitempty"\``
    and `ScreenAudioEnabled bool \`json:"screen_audio_enabled"\`` (no `omitempty` —
    the enabled flags in this struct, `muted`/`video_enabled`/`screen_sharing`, are
    all always-emitted).
20. `MediaStatePayload`: `ScreenAudioSSRC uint32 \`json:"screen_audio_ssrc,omitempty"\``
    and `ScreenAudioEnabled bool \`json:"screen_audio_enabled"\`` (no `omitempty`,
    matching `ScreenSharing` — the flag must be sent in both directions incl. `false`).
21. `ParticipantLeftPayload`: `ScreenAudioSSRC uint32 \`json:"screen_audio_ssrc,omitempty"\``.
    (`SpeakingPayload` is intentionally left mic-keyed — no screen field.)

**`internal/voice/status/server.go`**
22. HTTP `Participant` struct: add `ScreenAudioSSRC` (`omitempty`) +
    `ScreenAudioEnabled` JSON fields; populate both in `roomDetail`.

### Control plane — parity + `enabled` plumbing

**Protos (edit `.proto`, then `make proto`):**
23. `api/proto/call/v1/call.proto`:
    - `JoinVoiceResponse`: `uint32 screen_audio_ssrc = 10;` (8,9 in use).
    - `Participant`: `uint32 screen_audio_ssrc = 8;` `bool screen_audio_enabled = 9;`.
    - `SetMediaPrefsRequest`: `bool screen_audio_enabled = 6;`.
    - `VoiceParticipant` (GetVoiceStatus): `bool screen_audio_enabled = 7;` (no ssrc — decision A).
24. `api/proto/stream/v1/stream.proto`:
    - `VoiceParticipantState`: `uint32 screen_audio_ssrc = 10;` `bool screen_audio_enabled = 11;`.
    - `VoiceStateChanged`: `bool screen_audio_enabled = 7;`.
25. `api/proto/dm/v1/dm.proto`: `JoinDMCallResponse` reuses `callv1.Participant`, so
    step 23 covers it. It has no top-level screen ssrc → nothing else to add.

**`internal/voiceassign/service.go`**
26. `VoiceSession` struct: add `ScreenAudioEnabled bool`.
27. `VoiceParticipant` struct: add `ScreenAudioEnabled bool`.
28. `createAssignment`: init `ScreenAudioEnabled: false`.
29. `UpdateMediaPrefs`: add `screenAudioEnabled bool` param; set `session.ScreenAudioEnabled`.
30. `GetVoiceParticipants`: set `ScreenAudioEnabled: sess.ScreenAudioEnabled`.
    (This single struct change auto-covers the DM path, since `dm.Service.JoinCall`
    returns `[]voiceassign.VoiceParticipant`.)

**`internal/call/handler.go`**
31. `SetMediaPrefs`: pass `req.ScreenAudioEnabled` to `UpdateMediaPrefs`; add
    `ScreenAudioEnabled: req.ScreenAudioEnabled` to the `VoiceStateChanged` event.
32. `JoinVoice`: set `ScreenAudioEnabled: p.ScreenAudioEnabled` on each built
    `callv1.Participant` (screen_audio_ssrc stays 0 — decorative).
33. `GetVoiceStatus`: set `ScreenAudioEnabled: p.ScreenAudioEnabled` on `VoiceParticipant`.

**`internal/call/snapshot.go`**
34. `ToParticipantState`: set `ScreenAudioEnabled: p.ScreenAudioEnabled`
    (screen_audio_ssrc stays 0, like the other SSRCs here).

**`internal/dm/handler.go`**
35. `JoinDMCall`: set `ScreenAudioEnabled: p.ScreenAudioEnabled` on each built
    `callv1.Participant`.

## Wire compatibility

All wire additions are additive: new `omitempty` JSON fields (old peers ignore them)
and new proto fields with fresh tag numbers (old clients ignore them; new fields
default to 0/false). `tools/voicetest` reads `WelcomePayload.ScreenSSRC` by name and
is unaffected. Pre-rename clients that never send `screen_audio_enabled` decode it as
`false` — correct default. No protocol-version bump required (v3 stays v3; this is the
same additive posture as the existing screen fields).

## Test plan

- **protocol_test.go:** JSON round-trip for `WelcomePayload`, `ParticipantInfo`,
  `MediaStatePayload`, `ParticipantLeftPayload` — assert `screen_audio_ssrc` /
  `screen_audio_enabled` serialize and parse. Assert `MediaStatePayload`'s
  `screen_audio_enabled` is present even when `false` (no `omitempty`).
- **session_test.go:** `CreateSession` allocates 4 distinct SSRCs and all 4 resolve
  via `GetBySSRC`; `RemoveSession` and `SweepInactive` unmap all 4;
  `InactiveInfo.ScreenAudioSSRC` is populated.
- **router_test.go (key behavioral test):** a sender with `Muted=true` — a packet on
  `ScreenAudioSSRC` is still routed to subscribers, while a packet on the mic `SSRC`
  is dropped. Guards the one real logic change.
- **handler:** `mediaSSRCMatchesSession` returns true for an audio packet on
  `ScreenAudioSSRC` and false for a bogus SSRC.

## Verification (before claiming done)

1. `make proto` completes clean (toolchain already validated:
   `PATH=$PATH:$(go env GOPATH)/bin`, the four `protoc-gen-*` plugins installed,
   `api/proto-deps/` present, `api/gen/` gitignored so regen adds no diff).
2. `go build ./...` and `go test ./...` pass.
3. **grep-parity sweep** (omission is the main risk): every `ScreenSSRC` /
   `screen_ssrc` site has a `screen_audio_*` sibling; every `screen_sharing` /
   `ScreenSharing` state site has a `screen_audio_enabled` sibling. Note the two
   independent `ScreenSharing` stores (voiceassign via `SetMediaPrefs`; voice-session
   via UDP `MEDIA_STATE`) are both plumbed.

## Out of scope

Stereo desktop audio (channels negotiation + stereo playback worklet); any
`QUALITY_REPORT` / `BitrateHint` routing for the screen-audio SSRC (optional per
spec, deferred); client-side capture/downmix/decoder wiring (this is a backend spec).
