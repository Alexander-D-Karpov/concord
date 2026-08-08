package udp

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"sync"
	"time"

	voiceauth "github.com/Alexander-D-Karpov/concord/internal/voice/auth"
	"github.com/Alexander-D-Karpov/concord/internal/voice/congestion"
	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
	"github.com/Alexander-D-Karpov/concord/internal/voice/router"
	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
	"github.com/Alexander-D-Karpov/concord/internal/voice/telemetry"
	"go.uber.org/zap"
)

const (
	// migrationCooldown is the minimum interval between address migrations for one
	// session, rate-limiting migration attempts even after a valid decrypt.
	migrationCooldown = 1 * time.Second
	// migrateMapMaxSize is the lastMigrate map size that triggers age-based pruning,
	// bounding memory under churn.
	migrateMapMaxSize = 4096
	// migrateEntryMaxAge is how old a lastMigrate entry must be to be pruned once the
	// map exceeds migrateMapMaxSize.
	migrateEntryMaxAge = 1 * time.Minute
)

// Handler is the shared packet-processing core for every transport (UDP pool,
// legacy socket, TCP fallback). It owns no sockets; callers pass the reply
// conn/transport per packet. helloGate sheds handshake floods and lastMigrate
// (guarded by migrateMu) enforces the per-session migration cooldown. ctrl may be
// nil, disabling congestion-driven behavior.
type Handler struct {
	sessionManager *session.Manager
	router         *router.Router
	validator      *voiceauth.Validator
	logger         *zap.Logger
	metrics        *telemetry.Metrics
	ctrl           *congestion.Controller
	helloGate      *helloGate

	migrateMu   sync.Mutex
	lastMigrate map[uint32]time.Time
}

// NewHandler wires the handler's dependencies and initializes the hello gate and
// migration-tracking map. ctrl may be nil to run without congestion control.
func NewHandler(sessionManager *session.Manager, voiceRouter *router.Router, validator *voiceauth.Validator, logger *zap.Logger, metrics *telemetry.Metrics, ctrl *congestion.Controller) *Handler {
	return &Handler{
		sessionManager: sessionManager,
		router:         voiceRouter,
		validator:      validator,
		logger:         logger,
		metrics:        metrics,
		ctrl:           ctrl,
		helloGate:      newHelloGate(helloRatePerSec, helloBurst),
		lastMigrate:    make(map[uint32]time.Time),
	}
}

// handlePacket is the central dispatch: it records the receive metric and routes
// on the first byte to the per-type handler. owner (if set) is the pooled buffer
// forwarded to the router for zero-copy fan-out of media; tp is the reply
// transport for TCP-origin packets. Unknown types are silently ignored.
func (h *Handler) handlePacket(data []byte, owner router.PacketOwner, addr *net.UDPAddr, conn *net.UDPConn, tp session.Transport) {
	if len(data) < 1 {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordPacketReceived(uint64(len(data)))
	}

	switch data[0] {
	case protocol.PacketTypeHello:
		h.handleHello(data, addr, conn, tp)
	case protocol.PacketTypeAudio, protocol.PacketTypeVideo:
		h.handleMedia(data, owner, addr, conn)
	case protocol.PacketTypePing:
		h.handlePing(data, addr, conn)
	case protocol.PacketTypeBye:
		h.handleBye(data, addr, conn)
	case protocol.PacketTypeSpeaking:
		h.handleSpeaking(data, addr, conn)
	case protocol.PacketTypeMediaState:
		h.handleMediaState(data, addr, conn)
	case protocol.PacketTypeNack:
		h.handleNack(data, addr, conn)
	case protocol.PacketTypePli:
		h.handlePli(data, addr, conn)
	case protocol.PacketTypeRR:
		h.handleReceiverReport(data, addr, conn)
	case protocol.PacketTypeSubscribe:
		h.handleSubscribe(data, addr, conn)
	case protocol.PacketTypeQualityReport:
		h.handleQualityReport(data, addr, conn)
	case protocol.PacketTypeQualityPref:
		h.handleQualityPref(data, addr, conn)
	}
}

// HandlePacket dispatches a datagram with no owning buffer; media is forwarded by
// copy-free reference to data, which must stay valid until this returns.
func (h *Handler) HandlePacket(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	h.handlePacket(data, nil, addr, conn, nil)
}

// HandlePacketOwned dispatches a datagram backed by a reference-counted buffer, so
// the router can Retain it and fan media out without copying.
func (h *Handler) HandlePacketOwned(data []byte, owner router.PacketOwner, addr *net.UDPAddr, conn *net.UDPConn) {
	h.handlePacket(data, owner, addr, conn, nil)
}

// HandleFramedTCP dispatches a packet that arrived over a TCP transport. addr is
// a synthetic key (the TCP remote) used for session indexing; replies go via tp.
func (h *Handler) HandleFramedTCP(data []byte, addr *net.UDPAddr, tp session.Transport) {
	h.handlePacket(data, nil, addr, nil, tp)
}

// mediaSSRCMatchesSession verifies the packet's SSRC belongs to sess for its media
// type (audio must be the audio SSRC; video may be the camera or screen SSRC),
// rejecting a zero SSRC. It stops a session from injecting media under another
// stream's SSRC.
func mediaSSRCMatchesSession(sess *session.Session, packetType uint8, ssrc uint32) bool {
	switch packetType {
	case protocol.PacketTypeAudio:
		return ssrc != 0 && ssrc == sess.SSRC
	case protocol.PacketTypeVideo:
		return ssrc != 0 && (ssrc == sess.VideoSSRC || ssrc == sess.ScreenSSRC)
	default:
		return false
	}
}

// decrypt-verify proves the sender holds the session key before the address
// binding is moved; without it any host that learns an SSRC could hijack a stream
func (h *Handler) tryMigrateByMedia(hdr *protocol.MediaHeader, data []byte, addr *net.UDPAddr) *session.Session {
	sess := h.sessionManager.GetBySSRC(hdr.SSRC)
	if sess == nil || sess.IsObserver {
		return nil
	}
	if !mediaSSRCMatchesSession(sess, hdr.Type, hdr.SSRC) {
		return nil
	}
	if len(data) < protocol.MediaHeaderSize+crypto.AuthTagSize+1 {
		return nil
	}

	h.migrateMu.Lock()
	if last, ok := h.lastMigrate[sess.ID]; ok && time.Since(last) < migrationCooldown {
		h.migrateMu.Unlock()
		return nil
	}
	h.migrateMu.Unlock()

	sc := sess.CryptoForKeyID(hdr.KeyID)
	if sc == nil {
		return nil
	}

	if _, err := sc.DecryptSSRC(data[:protocol.MediaHeaderSize], data[protocol.MediaHeaderSize:], hdr.Counter, hdr.SSRC); err != nil {
		return nil
	}

	now := time.Now()
	h.migrateMu.Lock()
	h.lastMigrate[sess.ID] = now
	if len(h.lastMigrate) > migrateMapMaxSize {
		for id, t := range h.lastMigrate {
			if now.Sub(t) > migrateEntryMaxAge {
				delete(h.lastMigrate, id)
			}
		}
	}
	h.migrateMu.Unlock()

	h.sessionManager.BindAddr(sess.ID, addr)
	if h.metrics != nil {
		h.metrics.RecordMigration()
	}
	h.logger.Info("session address migrated",
		zap.String("user_id", sess.UserID),
		zap.String("room_id", sess.RoomID),
		zap.Uint32("ssrc", hdr.SSRC),
		zap.String("new_addr", addr.String()),
	)
	return sess
}

// handleMedia is the media fast path. It binds the packet to a session by source
// address (or, if unknown, attempts a decrypt-verified address migration), rejects
// mismatched/observer streams, refreshes activity, and updates metrics. For video
// it caches the packet for retransmit, tracks sequence gaps, and emits a
// (throttled) PLI on heavy loss; when congestion control is active it may reply
// with a BitrateHint. Finally it forwards to the router, owned or raw.
func (h *Handler) handleMedia(data []byte, owner router.PacketOwner, addr *net.UDPAddr, conn *net.UDPConn) {
	if len(data) < protocol.MediaHeaderSize {
		return
	}

	hdr, err := protocol.ParseMediaHeader(data)
	if err != nil {
		return
	}

	sess := h.sessionManager.GetByAddr(addr)
	if sess == nil {
		sess = h.tryMigrateByMedia(hdr, data, addr)
		if sess == nil {
			h.logger.Debug("media dropped: no session for addr",
				zap.String("addr", addr.String()),
				zap.Uint32("ssrc", hdr.SSRC),
				zap.Uint8("keyID", hdr.KeyID))
			return
		}
	}
	if sess.IsObserver {
		return
	}
	if !mediaSSRCMatchesSession(sess, hdr.Type, hdr.SSRC) {
		return
	}

	if h.sessionManager.Touch(sess.ID) {
		h.broadcastMediaState(sess.RoomID, sess, conn)
	}

	if h.metrics != nil {
		if hdr.Type == protocol.PacketTypeAudio {
			h.metrics.RecordAudioIn()
		} else {
			h.metrics.RecordVideoIn()
		}
	}

	if hdr.Type == protocol.PacketTypeVideo {
		sess.StoreForRetransmit(hdr.SSRC, hdr.Sequence, hdr.Timestamp, hdr.IsKeyframe(), data)

		sess.Mu.Lock()
		tracker := sess.SeqTrackers[hdr.SSRC]
		if tracker == nil {
			tracker = &session.SeqTracker{}
			sess.SeqTrackers[hdr.SSRC] = tracker
		}
		gap := tracker.Feed(hdr.Sequence)
		lossRate := tracker.LossRate()
		sess.Mu.Unlock()

		if gap > 3 && lossRate > 0.10 && !hdr.IsKeyframe() {
			if h.ctrl == nil || h.ctrl.AllowPLI(hdr.SSRC, time.Now()) {
				pliPkt := protocol.BuildPli(hdr.SSRC)
				_ = h.router.RouteControlToSession(pliPkt, conn, sess)
			} else if h.metrics != nil {
				h.metrics.RecordPliThrottled()
			}

			sess.Mu.Lock()
			if t := sess.SeqTrackers[hdr.SSRC]; t != nil {
				t.Reset()
			}
			sess.Mu.Unlock()
		}
	}

	if h.ctrl != nil {
		_, _, maxBr := sess.GetCapabilities()
		if target, reason, changed := h.ctrl.EvaluateBitrate(hdr.SSRC, hdr.Codec, maxBr, time.Now()); changed {
			_ = h.replyToSession(sess, addr, conn, protocol.BuildBitrateHint(hdr.SSRC, target, reason))
		}
	}

	if owner != nil {
		h.router.RouteMediaOwned(*hdr, data, owner, addr, conn)
	} else {
		h.router.RouteMediaRaw(*hdr, data, addr, conn)
	}
}

// buildSessionCrypto derives per-SSRC session crypto from a Hello's key material,
// returning nil for observers, a missing Crypto block, or a wrong-sized key (which
// leaves the session unencrypted rather than failing the handshake).
func buildSessionCrypto(hello *protocol.HelloPayload, roomID string, isObserver bool) *crypto.SessionCrypto {
	if isObserver || hello.Crypto == nil || len(hello.Crypto.KeyMaterial) != crypto.KeySize {
		return nil
	}
	var keyID uint8
	if len(hello.Crypto.KeyID) > 0 {
		keyID = hello.Crypto.KeyID[0]
	}
	// Derive the nonce base per-SSRC (HKDF) instead of trusting the client's
	// shared nonce base — removes cross-sender GCM nonce reuse.
	sc, _ := crypto.NewSessionCryptoDerived(hello.Crypto.KeyMaterial, roomID, keyID)
	return sc
}

// applyCapabilities copies the Hello's advertised FEC/DTX/max-bitrate onto the
// session, honoring the legacy Opus* field aliases. A Hello with no Capabilities
// leaves existing session caps unchanged.
func applyCapabilities(sess *session.Session, hello *protocol.HelloPayload) {
	if hello.Capabilities == nil {
		return
	}
	c := hello.Capabilities
	sess.SetCapabilities(c.FEC || c.OpusFEC, c.DTX || c.OpusDTX, c.MaxBitrate)
}

// negotiateProtocol answers a Hello with a version the client actually speaks.
// Everything v3 added to the wire is additive (new packet type 0x11, omitempty
// JSON fields), so a v2 client is served correctly by a v2 Welcome — whereas an
// unsolicited "protocol: 3" makes a strict v2 client discard the Welcome and
// re-Hello forever. A client claiming a version we don't know is clamped down.
func negotiateProtocol(clientProto uint8) uint8 {
	if clientProto == 0 || clientProto > protocol.ProtocolVersion {
		return protocol.ProtocolVersion
	}
	return clientProto
}

// buildWelcome assembles the Welcome reply for sess at the negotiated proto,
// including the current room roster. RRIntervalMs (receiver-report cadence) is
// only set for proto >= 3.
func (h *Handler) buildWelcome(sess *session.Session, proto uint8) protocol.WelcomePayload {
	w := protocol.WelcomePayload{
		Protocol:       proto,
		SessionID:      sess.ID,
		RoomID:         sess.RoomID,
		UserID:         sess.UserID,
		SSRC:           sess.SSRC,
		VideoSSRC:      sess.VideoSSRC,
		ScreenSSRC:     sess.ScreenSSRC,
		PingIntervalMs: 5000,
		Observer:       sess.IsObserver,
		Participants:   h.roomParticipants(sess.RoomID, sess.ID),
	}
	if proto >= 3 {
		w.RRIntervalMs = 250
	}
	return w
}

// roomParticipants snapshots the room roster for a Welcome, omitting observers and
// the session identified by excludeSessionID (normally the recipient itself), and
// folding in each peer's latest quality stats and capabilities.
func (h *Handler) roomParticipants(roomID string, excludeSessionID uint32) []protocol.ParticipantInfo {
	sessions := h.sessionManager.GetRoomSessions(roomID)
	participants := make([]protocol.ParticipantInfo, 0, len(sessions))
	for _, s := range sessions {
		if s == nil || s.IsObserver || s.ID == excludeSessionID {
			continue
		}
		quality, rttMs, packetLoss, jitterMs := s.SnapshotQuality()
		fec, dtx, maxBr := s.GetCapabilities()
		participants = append(participants, protocol.ParticipantInfo{
			UserID:        s.UserID,
			SSRC:          s.SSRC,
			VideoSSRC:     s.VideoSSRC,
			ScreenSSRC:    s.ScreenSSRC,
			Muted:         s.Muted,
			VideoEnabled:  s.VideoEnabled,
			ScreenSharing: s.ScreenSharing,
			Speaking:      s.Speaking,
			Quality:       quality,
			RTTMs:         rttMs,
			PacketLoss:    packetLoss,
			JitterMs:      jitterMs,
			FEC:           fec,
			DTX:           dtx,
			MaxBitrate:    maxBr,
		})
	}
	return participants
}

// requestKeyframesFrom sends a (congestion-throttled) PLI to every existing video
// and screen-share sender in the room except excludeSessionID, so a freshly joined
// receiver gets a decodable keyframe without waiting for the sender's own cadence.
func (h *Handler) requestKeyframesFrom(roomID string, excludeSessionID uint32, conn *net.UDPConn) {
	for _, peer := range h.sessionManager.GetRoomSessions(roomID) {
		if peer == nil || peer.IsObserver || peer.ID == excludeSessionID {
			continue
		}
		if peer.VideoSSRC != 0 && (h.ctrl == nil || h.ctrl.AllowPLI(peer.VideoSSRC, time.Now())) {
			_ = h.router.RouteControlToSession(protocol.BuildPli(peer.VideoSSRC), conn, peer)
		}
		if peer.ScreenSSRC != 0 && (h.ctrl == nil || h.ctrl.AllowPLI(peer.ScreenSSRC, time.Now())) {
			_ = h.router.RouteControlToSession(protocol.BuildPli(peer.ScreenSSRC), conn, peer)
		}
	}
}

// handleHello runs the join handshake: rate-limit by IP, parse and JWT-validate,
// negotiate protocol, and reconcile with any existing session for the user in the
// room (observer<->active transitions, or rebind). On a genuinely new join it
// creates the session, replies with Welcome, requests keyframes from peers, and
// broadcasts the join. Silently returns on rate-limit, bad JSON, or invalid token.
func (h *Handler) handleHello(data []byte, addr *net.UDPAddr, conn *net.UDPConn, tp session.Transport) {
	// Shed HELLO floods per source IP before the expensive JWT validation.
	if addr != nil && h.helloGate != nil && !h.helloGate.allow(addr.IP.String(), time.Now()) {
		if h.metrics != nil {
			h.metrics.RecordHelloThrottled()
		}
		h.logger.Debug("hello rate-limited", zap.String("ip", addr.IP.String()))
		return
	}
	var hello protocol.HelloPayload
	if err := json.Unmarshal(data[1:], &hello); err != nil {
		h.logger.Warn("failed to unmarshal hello", zap.Error(err))
		return
	}
	if h.metrics != nil {
		h.metrics.RecordHello()
	}

	proto := negotiateProtocol(hello.Protocol)
	h.logger.Debug("hello received",
		zap.String("addr", addrString(addr)),
		zap.Uint8("client_protocol", hello.Protocol),
		zap.Uint8("negotiated_protocol", proto),
		zap.Bool("observer", hello.Observer),
	)

	claims, err := h.validator.ValidateToken(context.Background(), hello.Token)
	if err != nil {
		h.logger.Warn("invalid token in hello", zap.Error(err))
		return
	}

	isObserver := hello.Observer
	sessionCrypto := buildSessionCrypto(&hello, claims.RoomID, isObserver)

	if existing := h.sessionManager.GetSessionByUserInRoom(claims.UserID, claims.RoomID); existing != nil {
		switch {
		case existing.IsObserver && !isObserver:
			h.sessionManager.RemoveSession(existing.ID)
		case !existing.IsObserver && isObserver:
			h.sendWelcomeForObserver(existing, proto, addr, conn, tp)
			return
		default:
			h.rebindSession(existing, &hello, proto, sessionCrypto, addr, conn, tp)
			return
		}
	}

	sess := h.sessionManager.CreateSession(claims.UserID, claims.RoomID, addr, sessionCrypto, hello.VideoEnabled, isObserver)
	applyCapabilities(sess, &hello)
	sess.SetTransport(tp)

	out, err := protocol.BuildJSONPacket(protocol.PacketTypeWelcome, h.buildWelcome(sess, proto))
	if err != nil {
		return
	}
	if err := h.replyToSession(sess, addr, conn, out); err != nil {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordWelcome()
	}

	if !isObserver {
		h.requestKeyframesFrom(claims.RoomID, sess.ID, conn)
		h.broadcastJoined(claims.RoomID, sess, conn)
	}

	h.logger.Info("session created",
		zap.String("user_id", claims.UserID),
		zap.String("room_id", claims.RoomID),
		zap.Uint32("ssrc_audio", sess.SSRC),
		zap.Uint32("ssrc_video", sess.VideoSSRC),
		zap.Uint32("ssrc_screen", sess.ScreenSSRC),
		zap.Uint8("protocol", proto),
		zap.Bool("observer", isObserver),
	)
}

// rebindSession handles a repeat Hello from an existing session (reconnect or
// address change): it moves the address binding, refreshes activity, updates
// transport/crypto/video/capabilities, re-sends Welcome, and (for non-observers)
// re-requests keyframes and rebroadcasts media state. SSRCs are preserved so the
// client keeps its identity.
func (h *Handler) rebindSession(sess *session.Session, hello *protocol.HelloPayload, proto uint8, sc *crypto.SessionCrypto, addr *net.UDPAddr, conn *net.UDPConn, tp session.Transport) {
	if h.metrics != nil {
		h.metrics.RecordRebind()
	}
	h.sessionManager.BindAddr(sess.ID, addr)
	h.sessionManager.Touch(sess.ID)
	if tp != nil {
		sess.SetTransport(tp)
	}

	if sc != nil {
		sess.SetCrypto(sc)
	}
	if !sess.IsObserver {
		sess.SetVideoEnabled(hello.VideoEnabled)
	}
	applyCapabilities(sess, hello)

	out, err := protocol.BuildJSONPacket(protocol.PacketTypeWelcome, h.buildWelcome(sess, proto))
	if err != nil {
		return
	}
	if err := h.replyToSession(sess, addr, conn, out); err != nil {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordWelcome()
	}

	if !sess.IsObserver {
		h.requestKeyframesFrom(sess.RoomID, sess.ID, conn)
		h.broadcastMediaState(sess.RoomID, sess, conn)
	}

	h.logger.Info("session rebound",
		zap.String("user_id", sess.UserID),
		zap.String("room_id", sess.RoomID),
		zap.Uint32("ssrc", sess.SSRC),
		zap.Uint8("protocol", proto),
		zap.String("new_addr", addrString(addr)),
	)
}

// sendWelcomeForObserver replies to an observer Hello that collided with the
// user's existing active session: it returns a Welcome describing that active
// session (over tp if set, else the UDP socket) without creating a second session.
func (h *Handler) sendWelcomeForObserver(existingActive *session.Session, proto uint8, addr *net.UDPAddr, conn *net.UDPConn, tp session.Transport) {
	out, err := protocol.BuildJSONPacket(protocol.PacketTypeWelcome, h.buildWelcome(existingActive, proto))
	if err != nil {
		return
	}
	if tp != nil {
		_ = tp.WritePacket(out)
		return
	}
	_ = h.send(out, addr, conn)
}

// addrString formats addr for logging, returning "" for a nil address (TCP-origin
// packets have no UDP addr).
func addrString(addr *net.UDPAddr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

// handleQualityPref records a receiver's per-SSRC simulcast layer preferences,
// which the router later consults to drop layers above the requested tier. Ignored
// if the source address maps to no session.
func (h *Handler) handleQualityPref(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	sess := h.sessionManager.GetByAddr(addr)
	if sess == nil {
		return
	}
	var payload protocol.QualityPrefPayload
	if err := json.Unmarshal(data[1:], &payload); err != nil {
		return
	}
	h.sessionManager.Touch(sess.ID)
	for _, entry := range payload.Prefs {
		sess.SetQualityPref(entry.SSRC, entry.Tier)
	}
}

// handlePing replies with a Pong echoing the ping body (so the client can measure
// RTT) and refreshes the session's activity. Works even before a session exists,
// replying to the raw address.
func (h *Handler) handlePing(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	if h.metrics != nil {
		h.metrics.RecordPing()
	}
	sess := h.sessionManager.GetByAddr(addr)
	if sess != nil {
		h.sessionManager.Touch(sess.ID)
	}
	pong := make([]byte, len(data))
	pong[0] = protocol.PacketTypePong
	copy(pong[1:], data[1:])
	_ = h.replyToSession(sess, addr, conn, pong)
	if h.metrics != nil {
		h.metrics.RecordPong()
	}
}

// handleBye tears down a session on an explicit leave. It looks the session up by
// SSRC and ignores the packet if it did not come from that session's bound address
// (so a spoofed SSRC can't evict another user), then removes the session and
// broadcasts ParticipantLeft.
func (h *Handler) handleBye(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	if len(data) < 5 {
		return
	}
	ssrc := binary.BigEndian.Uint32(data[1:5])
	sess := h.sessionManager.GetBySSRC(ssrc)
	if sess == nil {
		return
	}
	if sess.AddrChanged(addr) {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordBye()
	}
	roomID := sess.RoomID
	userID := sess.UserID
	videoSSRC := sess.VideoSSRC
	screenSSRC := sess.ScreenSSRC
	h.sessionManager.RemoveSession(sess.ID)
	h.broadcastParticipantLeft(roomID, userID, ssrc, videoSSRC, screenSSRC, conn)
	h.logger.Info("session ended", zap.String("user_id", userID), zap.String("room_id", roomID))
}

// handleSpeaking updates a session's speaking state and relays it to the room. The
// packet is dropped if its SSRC's session is unknown or its source address does
// not match, and the relayed payload is rebuilt from server-side session fields so
// a client can't spoof another user's identity.
func (h *Handler) handleSpeaking(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	var speaking protocol.SpeakingPayload
	if err := json.Unmarshal(data[1:], &speaking); err != nil {
		return
	}
	sess := h.sessionManager.GetBySSRC(speaking.SSRC)
	if sess == nil || sess.AddrChanged(addr) {
		return
	}
	sess.SetSpeaking(speaking.Speaking)
	h.sessionManager.Touch(sess.ID)
	pkt, err := protocol.BuildJSONPacket(protocol.PacketTypeSpeaking, protocol.SpeakingPayload{
		SSRC:      sess.SSRC,
		VideoSSRC: sess.VideoSSRC,
		UserID:    sess.UserID,
		RoomID:    sess.RoomID,
		Speaking:  speaking.Speaking,
	})
	if err != nil {
		return
	}
	h.router.RouteControlRoom(pkt, conn, sess.RoomID, sess.ID)
}

// handleMediaState applies a sender's mute/video/screen-share toggle and
// rebroadcasts the authoritative state to the room. Dropped if the SSRC is unknown
// or the source address does not match the session.
func (h *Handler) handleMediaState(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	var ms protocol.MediaStatePayload
	if err := json.Unmarshal(data[1:], &ms); err != nil {
		return
	}
	sess := h.sessionManager.GetBySSRC(ms.SSRC)
	if sess == nil || sess.AddrChanged(addr) {
		return
	}
	sess.SetMuted(ms.Muted)
	sess.SetVideoEnabled(ms.VideoEnabled)
	sess.SetScreenSharing(ms.ScreenSharing)
	h.sessionManager.Touch(sess.ID)
	h.broadcastMediaState(sess.RoomID, sess, conn)
}

// handleNack serves a retransmit request: for each missing sequence still in the
// target SSRC's retransmit buffer, it resends the cached packet to the requester.
// Nothing is sent for sequences that have aged out. The requester is resolved by
// source address so the reply goes back to the asker.
func (h *Handler) handleNack(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	nack, err := protocol.ParseNack(data)
	if err != nil {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordNack()
	}
	target := h.sessionManager.GetBySSRC(nack.SSRC)
	if target == nil {
		return
	}
	requester := h.sessionManager.GetByAddr(addr)
	for _, seq := range nack.Sequences {
		if cached := target.GetForRetransmit(nack.SSRC, seq); cached != nil {
			if err := h.replyToSession(requester, addr, conn, cached); err == nil && h.metrics != nil {
				h.metrics.RecordRetransmit()
			}
		}
	}
}

// handlePli forwards a keyframe request to the video sender identified by the PLI's
// SSRC, subject to congestion throttling (a throttled PLI is dropped and counted).
func (h *Handler) handlePli(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	pli, err := protocol.ParsePli(data)
	if err != nil {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordPli()
	}
	target := h.sessionManager.GetBySSRC(pli.SSRC)
	if target == nil {
		return
	}
	if h.ctrl == nil || h.ctrl.AllowPLI(pli.SSRC, time.Now()) {
		_ = h.router.RouteControlToSession(data, conn, target)
	} else if h.metrics != nil {
		h.metrics.RecordPliThrottled()
	}
}

// handleReceiverReport forwards a receiver report to the reported stream's sender
// and, when congestion control is active, feeds the reporter's loss/jitter into
// the controller (which may later drive bitrate hints and layer selection).
func (h *Handler) handleReceiverReport(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	rr, err := protocol.ParseReceiverReport(data)
	if err != nil {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordReceiverReport()
	}
	target := h.sessionManager.GetBySSRC(rr.SSRC)
	if target == nil {
		return
	}
	_ = h.router.RouteControlToSession(data, conn, target)

	if h.ctrl != nil {
		if rep := h.sessionManager.GetBySSRC(rr.ReporterSSRC); rep != nil {
			h.ctrl.ObserveRR(rr.SSRC, rep.ID, rr.FractionLost, rr.Jitter, time.Now())
		}
	}
}

// handleSubscribe updates which source SSRCs a receiver wants forwarded. An empty
// subscription list is treated as premature/stale and ignored (see
// Session.UpdateSubscriptions) so a receiver is never accidentally blacked out.
func (h *Handler) handleSubscribe(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	sess := h.sessionManager.GetByAddr(addr)
	if sess == nil {
		return
	}
	var payload protocol.SubscribePayload
	if err := json.Unmarshal(data[1:], &payload); err != nil {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordSubscribe()
	}
	h.sessionManager.Touch(sess.ID)
	sess.UpdateSubscriptions(payload.Subscriptions)
	if len(payload.Subscriptions) == 0 {
		h.logger.Debug("ignoring empty subscription update (premature/stale)",
			zap.String("user_id", sess.UserID))
		return
	}
	h.logger.Info("subscriptions updated",
		zap.String("user_id", sess.UserID),
		zap.Uint32("session_ssrc", sess.SSRC),
		zap.Int("count", len(payload.Subscriptions)),
		zap.Uint32s("ssrcs", payload.Subscriptions),
	)
}

// handleQualityReport stores a client's self-reported quality on its session,
// records RTT metrics, and relays the report (with server-authoritative identity
// fields) to the rest of the room. Ignored if the source address has no session.
func (h *Handler) handleQualityReport(data []byte, addr *net.UDPAddr, conn *net.UDPConn) {
	var payload protocol.QualityReportPayload
	if err := json.Unmarshal(data[1:], &payload); err != nil {
		return
	}
	sess := h.sessionManager.GetByAddr(addr)
	if sess == nil {
		return
	}
	if h.metrics != nil {
		h.metrics.RecordQualityReport()
		if payload.RTTMs > 0 {
			h.metrics.RecordRTT(payload.RTTMs)
		}
	}
	payload.UserID = sess.UserID
	payload.RoomID = sess.RoomID
	payload.SSRC = sess.SSRC
	sess.SetQuality(payload.Quality, payload.RTTMs, payload.PacketLoss, payload.JitterMs)
	h.sessionManager.Touch(sess.ID)
	pkt, err := protocol.BuildJSONPacket(protocol.PacketTypeQualityReport, payload)
	if err != nil {
		return
	}
	h.router.RouteControlRoom(pkt, conn, sess.RoomID, sess.ID)
}

// broadcastJoined announces a newly created session to the room as a MediaState
// packet carrying its SSRCs and initial mute/video state, excluding the joiner
// itself.
func (h *Handler) broadcastJoined(roomID string, newSess *session.Session, conn *net.UDPConn) {
	pkt, err := protocol.BuildJSONPacket(protocol.PacketTypeMediaState, protocol.MediaStatePayload{
		SSRC:          newSess.SSRC,
		VideoSSRC:     newSess.VideoSSRC,
		ScreenSSRC:    newSess.ScreenSSRC,
		UserID:        newSess.UserID,
		RoomID:        roomID,
		Muted:         newSess.Muted,
		VideoEnabled:  newSess.VideoEnabled,
		ScreenSharing: newSess.ScreenSharing,
	})
	if err != nil {
		return
	}
	h.router.RouteControlRoom(pkt, conn, roomID, newSess.ID)
}

// broadcastParticipantLeft tells the whole room (exclude session 0, i.e. nobody)
// that a participant's streams are gone, so peers can drop them from the UI.
func (h *Handler) broadcastParticipantLeft(roomID, userID string, ssrc, videoSSRC, screenSSRC uint32, conn *net.UDPConn) {
	pkt, err := protocol.BuildJSONPacket(protocol.PacketTypeParticipantLeft, protocol.ParticipantLeftPayload{
		UserID:     userID,
		RoomID:     roomID,
		SSRC:       ssrc,
		VideoSSRC:  videoSSRC,
		ScreenSSRC: screenSSRC,
	})
	if err != nil {
		return
	}
	h.router.RouteControlRoom(pkt, conn, roomID, 0)
}

// broadcastMediaState relays a session's current mute/video/screen-share state to
// the room (excluding the session itself), used on resume/toggle to keep peers in
// sync.
func (h *Handler) broadcastMediaState(roomID string, sess *session.Session, conn *net.UDPConn) {
	pkt, err := protocol.BuildJSONPacket(protocol.PacketTypeMediaState, protocol.MediaStatePayload{
		SSRC:          sess.SSRC,
		VideoSSRC:     sess.VideoSSRC,
		ScreenSSRC:    sess.ScreenSSRC,
		UserID:        sess.UserID,
		RoomID:        roomID,
		Muted:         sess.Muted,
		VideoEnabled:  sess.VideoEnabled,
		ScreenSharing: sess.ScreenSharing,
	})
	if err != nil {
		return
	}
	h.router.RouteControlRoom(pkt, conn, roomID, sess.ID)
}

// send writes data to addr over conn and records the sent-bytes metric on success.
// It is the direct UDP path used by replyToSession when the session has no
// transport.
func (h *Handler) send(data []byte, addr *net.UDPAddr, conn *net.UDPConn) error {
	_, err := conn.WriteToUDP(data, addr)
	if err == nil && h.metrics != nil {
		h.metrics.RecordPacketSent(uint64(len(data)))
	}
	return err
}

// replyToSession sends to a session over its transport (TCP) when set, else over
// the UDP socket. Safe with a nil session or nil conn.
func (h *Handler) replyToSession(sess *session.Session, addr *net.UDPAddr, conn *net.UDPConn, data []byte) error {
	if sess != nil {
		if t := sess.Transport(); t != nil {
			return t.WritePacket(data)
		}
	}
	if conn != nil && addr != nil {
		return h.send(data, addr, conn)
	}
	return nil
}

// SweepAndNotify runs the two-stage inactivity sweep and broadcasts
// ParticipantLeft for sessions that just went inactive. Returns removed IDs.
func (h *Handler) SweepAndNotify(conn *net.UDPConn, inactiveAfter, removeAfter time.Duration) []uint32 {
	if h.helloGate != nil {
		h.helloGate.prune(time.Now(), 5*time.Minute)
	}
	nowInactive, removed := h.sessionManager.SweepInactive(inactiveAfter, removeAfter, time.Now())
	for _, info := range nowInactive {
		h.broadcastParticipantLeft(info.RoomID, info.UserID, info.SSRC, info.VideoSSRC, info.ScreenSSRC, conn)
	}
	return removed
}
