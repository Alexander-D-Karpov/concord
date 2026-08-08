package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	mrand "math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	authv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/auth/v1"
	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
	"google.golang.org/grpc/metadata"
)

// bot is a single simulated voice client: it authenticates over gRPC, joins voice,
// and speaks the real UDP media protocol. All wire framing and crypto come from the
// backend's internal/voice packages, so a bot is byte-compatible with a real client.
type bot struct {
	idx         int
	handle      string
	userID      string
	token       string
	roomID      string
	voiceToken  string
	udpHost     string
	udpPort     int
	ssrc        uint32
	videoSSRC   uint32
	screenSSRC  uint32
	keyMaterial []byte
	keyID       byte
	sc          *crypto.SessionCrypto // shared-code AES-256-GCM sealer (send path)
	conn        *net.UDPConn
	connMu      sync.RWMutex
	peerSSRCs   []uint32
	peerMu      sync.Mutex
	st          *stats
	ready       chan struct{}

	// Media-demo roles, set only when the TUI/media panel is active. The publisher
	// emits real tone+video; the monitor decrypts and taps the publisher's streams.
	role           botRole
	tone           *toneSource
	video          *videoSource
	tap            *mediaTap      // monitor: decoded publisher media lands here
	watchAudioSSRC atomic.Uint32  // monitor: publisher audio SSRC (0 until orchestrator sets it)
	watchVideoSSRC atomic.Uint32  // monitor: publisher video SSRC
	rxCipher       *crypto.Cipher // monitor: opens received media with the shared room key
}

// botRole selects a bot's part in the media demo (see the const block below).
type botRole uint8

const (
	// roleNoise bots only generate background media load; they neither publish the
	// demo streams nor tap them.
	roleNoise botRole = iota
	// rolePublisher emits the real synthetic tone/video that the monitor decodes.
	rolePublisher
	// roleMonitor decrypts and taps the publisher's streams for rendering/assertions.
	roleMonitor
)

// withRateLimitBypass attaches the rate-limit bypass token as gRPC metadata when the
// -rl-bypass-token flag is set, so bulk bot login/registration isn't throttled.
// Returns ctx unchanged when no token is configured.
func withRateLimitBypass(ctx context.Context) context.Context {
	if *rateLimitBypassToken == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "x-concord-ratelimit-bypass", *rateLimitBypassToken)
}

// loginOrRegister tries a password login for handle h and, if that fails, registers a
// new account, returning an access token. It combines both errors when registration
// also fails, so a bot can be provisioned on first run and reused afterward.
func loginOrRegister(ctx context.Context, c authv1.AuthServiceClient, h, p string) (string, error) {
	r, err := c.LoginPassword(ctx, &authv1.LoginPasswordRequest{Handle: h, Password: p})
	if err == nil {
		return r.AccessToken, nil
	}
	r2, err2 := c.Register(ctx, &authv1.RegisterRequest{Handle: h, Password: p, DisplayName: "Bot " + h})
	if err2 != nil {
		return "", fmt.Errorf("login: %v; register: %v", err, err2)
	}
	return r2.AccessToken, nil
}

// withAuth attaches token as a Bearer authorization header on outgoing gRPC calls.
func withAuth(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

// getConn returns the bot's current UDP socket under a read lock, since a churn or
// netchange loop may swap it concurrently.
func (b *bot) getConn() *net.UDPConn {
	b.connMu.RLock()
	defer b.connMu.RUnlock()
	return b.conn
}

// setConn atomically swaps in a new UDP socket, closing the previous one if any.
func (b *bot) setConn(c *net.UDPConn) {
	b.connMu.Lock()
	old := b.conn
	b.conn = c
	b.connMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// dial opens the bot's UDP socket to the assigned voice endpoint with 4 MiB read/write
// buffers (headroom for burst media) and installs it as the current connection.
func (b *bot) dial() error {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", b.udpHost, b.udpPort))
	if err != nil {
		return err
	}
	c, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return err
	}
	c.SetReadBuffer(4 << 20)
	c.SetWriteBuffer(4 << 20)
	b.setConn(c)
	return nil
}

// sendImpaired writes a packet applying client-side loss/jitter/reorder to
// exercise the server's loss handling and jitter tolerance.
func (b *bot) sendImpaired(pkt []byte, counter *atomic.Uint64) {
	if *lossRate > 0 && mrand.Float64() < *lossRate {
		return // simulated loss
	}
	send := func(p []byte) {
		if _, err := b.getConn().Write(p); err != nil {
			b.st.errors.Add(1)
			return
		}
		counter.Add(1)
		b.st.bytesOut.Add(uint64(len(p)))
	}
	if *reorderRate > 0 && mrand.Float64() < *reorderRate {
		cp := append([]byte(nil), pkt...)
		go func() {
			time.Sleep(time.Duration(5+mrand.Intn(20)) * time.Millisecond)
			send(cp)
		}()
		return
	}
	if *jitterMaxMs > 0 {
		cp := append([]byte(nil), pkt...)
		d := time.Duration(mrand.Intn(*jitterMaxMs+1)) * time.Millisecond
		go func() { time.Sleep(d); send(cp) }()
		return
	}
	send(pkt)
}

// rebindSocket dials a fresh local UDP socket to the same server, so the next
// media packet arrives from a new source address and triggers server migration.
func (b *bot) rebindSocket() {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", b.udpHost, b.udpPort))
	if err != nil {
		return
	}
	c, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return
	}
	c.SetReadBuffer(4 << 20)
	c.SetWriteBuffer(4 << 20)
	b.setConn(c)
}

// churnLoop, when -churn is set, periodically sends a Bye then re-sends Hello to
// leave and immediately rejoin voice (reusing the still-valid voice token), exercising
// the server's session teardown/rejoin path. It is a no-op when -churn is 0.
func (b *bot) churnLoop(ctx context.Context) {
	if *churnEvery <= 0 {
		return
	}
	t := time.NewTicker(*churnEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			bye := make([]byte, 5)
			bye[0] = protocol.PacketTypeBye
			binary.BigEndian.PutUint32(bye[1:], b.ssrc)
			_, _ = b.getConn().Write(bye)
			time.Sleep(50 * time.Millisecond)
			_ = b.hello() // re-join with the still-valid voice token
		}
	}
}

// netchangeLoop, when -netchange is set, periodically rebinds the bot's UDP socket so
// subsequent packets arrive from a new source address, exercising server-side session
// migration. It is a no-op when -netchange is 0.
func (b *bot) netchangeLoop(ctx context.Context) {
	if *netChangeEvery <= 0 {
		return
	}
	t := time.NewTicker(*netChangeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.rebindSocket()
		}
	}
}

// rrLoop periodically sends a Receiver Report for each peer stream, reporting
// -report-loss, which drives the server's RR aggregation → BitrateHint back to
// the sender — exercising the congestion feedback loop end to end.
func (b *bot) rrLoop(ctx context.Context) {
	if *reportLoss <= 0 {
		return
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.peerMu.Lock()
			peers := append([]uint32(nil), b.peerSSRCs...)
			b.peerMu.Unlock()
			for _, ssrc := range peers {
				_, _ = b.getConn().Write(protocol.BuildReceiverReport(protocol.ReceiverReport{
					SSRC:         ssrc,
					ReporterSSRC: b.ssrc,
					FractionLost: *reportLoss,
				}))
			}
		}
	}
}

// hello sends the UDP Hello handshake that joins the room: it carries the voice token,
// protocol version, codecs, the room key material/AEAD crypto info, and advertised
// capabilities (FEC/DTX/max bitrate) from the flags. The server replies with a Welcome
// (handled in recvLoop).
func (b *bot) hello() error {
	payload := protocol.HelloPayload{
		Token:        b.voiceToken,
		Protocol:     protocol.ProtocolVersion,
		Codec:        "opus",
		RoomID:       b.roomID,
		UserID:       b.userID,
		VideoEnabled: *sendVideo,
		VideoCodec:   "h264",
		Crypto: &protocol.CryptoInfo{
			AEAD:        "aes-256-gcm",
			KeyID:       []byte{b.keyID},
			KeyMaterial: b.keyMaterial,
		},
		Capabilities: &protocol.Capabilities{
			FEC:        *fec,
			DTX:        *dtx,
			MaxBitrate: uint32(*maxBitrate),
		},
	}
	pkt, err := protocol.BuildJSONPacket(protocol.PacketTypeHello, payload)
	if err != nil {
		return err
	}
	_, err = b.getConn().Write(pkt)
	return err
}

// recvLoop reads inbound UDP packets until ctx is cancelled, using a short read
// deadline so it can poll ctx. It dispatches by packet type: Welcome records the
// assigned SSRCs and peer list and closes b.ready; Audio/Video bump counters and, for a
// monitor, feed the media tap; Pong computes RTT; BitrateHint records congestion
// feedback. Timeouts are ignored; other read errors bump the error counter.
func (b *bot) recvLoop(ctx context.Context) {
	buf := make([]byte, 64*1024)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c := b.getConn()
		c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, err := c.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
			b.st.errors.Add(1)
			continue
		}

		if n < 1 {
			continue
		}

		b.st.bytesIn.Add(uint64(n))

		switch buf[0] {
		case protocol.PacketTypeWelcome:
			w, err := protocol.ParseJSON[protocol.WelcomePayload](buf[1:n])
			if err != nil {
				log.Printf("[BOT %d] bad welcome json (%d bytes): %v", b.idx, n, err)
				continue
			}
			b.ssrc = w.SSRC
			b.videoSSRC = w.VideoSSRC
			b.screenSSRC = w.ScreenSSRC
			var peers []uint32
			for _, p := range w.Participants {
				if p.SSRC != 0 {
					peers = append(peers, p.SSRC)
				}
			}
			b.peerMu.Lock()
			b.peerSSRCs = peers
			b.peerMu.Unlock()
			b.st.welcomeOK.Add(1)
			log.Printf("[BOT %d] welcome ssrc=%d video=%d screen=%d", b.idx, b.ssrc, b.videoSSRC, b.screenSSRC)
			select {
			case <-b.ready:
			default:
				close(b.ready)
			}

		case protocol.PacketTypeAudio:
			b.st.audioRecv.Add(1)
			if b.tap != nil {
				b.tapAudio(buf[:n])
			}

		case protocol.PacketTypeVideo:
			b.st.videoRecv.Add(1)
			if b.tap != nil {
				b.tapVideo(buf[:n])
			}

		case protocol.PacketTypePong:
			if n >= 9 {
				sent := int64(binary.BigEndian.Uint64(buf[1:9]))
				rtt := time.Duration(time.Now().UnixMilli()-sent) * time.Millisecond
				b.st.pongRecv.Add(1)
				b.st.addRTT(rtt)
			}

		case protocol.PacketTypeBitrateHint:
			if hint, herr := protocol.ParseBitrateHint(buf[:n]); herr == nil {
				b.st.bitrateHints.Add(1)
				b.st.lastBitrate.Store(uint64(hint.TargetBps))
			}

		case protocol.PacketTypeHello, protocol.PacketTypeBye:
			// ignore control packets not needed by the stress tool
		}
	}
}

// tapAudio decrypts a received audio packet from the watched publisher and hands the
// PCM to the media tap. Non-publisher SSRCs (noise bots, the monitor's own audio) are
// ignored so only the publisher's real tone is rendered.
func (b *bot) tapAudio(pkt []byte) {
	watch := b.watchAudioSSRC.Load()
	if watch == 0 {
		return
	}
	hdr, err := protocol.ParseMediaHeader(pkt)
	if err != nil || hdr.SSRC != watch {
		return
	}
	pt, err := b.openMedia(pkt, hdr)
	if err != nil {
		b.tap.decodeErr()
		return
	}
	b.tap.putAudio(pt)
}

// tapVideo decrypts and decodes a received video packet from the watched publisher.
func (b *bot) tapVideo(pkt []byte) {
	watch := b.watchVideoSSRC.Load()
	if watch == 0 {
		return
	}
	hdr, err := protocol.ParseMediaHeader(pkt)
	if err != nil || hdr.SSRC != watch {
		return
	}
	pt, err := b.openMedia(pkt, hdr)
	if err != nil {
		b.tap.decodeErr()
		return
	}
	f, err := decodeFrame(pt)
	if err != nil {
		b.tap.decodeErr()
		return
	}
	b.tap.putVideo(f)
}

// openMedia opens a received media packet with the shared room key and the sender's
// per-SSRC nonce base. It uses the bare Cipher (no replay filter) because the monitor
// decodes interleaved audio+video SSRCs whose counters both start near zero — a
// shared replay window would false-reject one of the streams.
func (b *bot) openMedia(pkt []byte, hdr *protocol.MediaHeader) ([]byte, error) {
	if b.rxCipher == nil {
		return nil, fmt.Errorf("no receive cipher")
	}
	if len(pkt) < protocol.MediaHeaderSize {
		return nil, fmt.Errorf("short packet")
	}
	aad := pkt[:protocol.MediaHeaderSize]
	ct := pkt[protocol.MediaHeaderSize:]
	base := crypto.DeriveNonceBase(b.keyMaterial, b.roomID, hdr.KeyID, hdr.SSRC)
	return b.rxCipher.OpenWithBase(aad, ct, base, hdr.Counter)
}

// audioLoop waits for the Welcome (or times out after 10s), then sends an audio packet
// every -audio-rate ms until ctx is cancelled. A publisher emits its real tone frames;
// other bots send fixed fake Opus. The RTP-like sequence, counter and timestamp advance
// each frame (timestamp by 960 = 20ms at 48kHz).
func (b *bot) audioLoop(ctx context.Context) {
	select {
	case <-b.ready:
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
		log.Printf("[BOT %d] timeout waiting for welcome, skipping audio", b.idx)
		return
	}

	if b.ssrc == 0 {
		log.Printf("[BOT %d] ssrc=0, skip audio", b.idx)
		return
	}

	tick := time.NewTicker(time.Duration(*audioRateMs) * time.Millisecond)
	defer tick.Stop()

	fakeOpus := make([]byte, 160)
	rand.Read(fakeOpus)

	var seq uint16
	var ctr uint64
	var ts uint32

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			payload := fakeOpus
			if b.tone != nil { // publisher: emit a real tone the monitor can render
				payload = b.tone.frame(int(ctr))
			}
			pkt := b.mediaPkt(protocol.PacketTypeAudio, 0, protocol.CodecOpus, b.ssrc, seq, ts, ctr, payload)
			b.sendImpaired(pkt, &b.st.audioSent)
			seq++
			ctr++
			ts += 960
		}
	}
}

// vidLoop waits for the Welcome (or times out after 3s), then sends a video packet
// every -video-rate ms until ctx is cancelled. A publisher emits real animated frames
// (every one a keyframe); other bots send fake H264 with a synthetic keyframe every
// 90th frame. Returns immediately if no video SSRC was assigned.
func (b *bot) vidLoop(ctx context.Context) {
	select {
	case <-b.ready:
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Second):
		return
	}

	if b.videoSSRC == 0 {
		return
	}

	tick := time.NewTicker(time.Duration(*videoRateMs) * time.Millisecond)
	defer tick.Stop()

	fake := make([]byte, 800)
	rand.Read(fake)

	var seq uint16
	var ctr uint64
	var ts uint32
	var fc int

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			payload := fake
			var flags uint8
			if b.video != nil { // publisher: emit a real animated frame, always a keyframe
				payload = encodeFrame(b.video.frame(fc))
				flags = protocol.FlagKeyframe
			} else if fc%90 == 0 {
				flags = protocol.FlagKeyframe
			}
			pkt := b.mediaPkt(protocol.PacketTypeVideo, flags, protocol.CodecH264, b.videoSSRC, seq, ts, ctr, payload)
			b.sendImpaired(pkt, &b.st.videoSent)
			seq++
			ctr++
			ts += 3000
			fc++
		}
	}
}

// pingLoop sends a Ping carrying the current millisecond timestamp every 5s; the
// matching Pong (handled in recvLoop) yields an RTT sample. Runs until ctx is cancelled.
func (b *bot) pingLoop(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			pkt := make([]byte, 9)
			pkt[0] = protocol.PacketTypePing
			binary.BigEndian.PutUint64(pkt[1:], uint64(time.Now().UnixMilli()))
			b.getConn().Write(pkt)
		}
	}
}

// mediaPkt assembles one media packet exactly as production clients do: a 24-byte
// protocol.MediaHeader followed by the AES-256-GCM ciphertext, with the marshaled
// header as AAD so peers authenticate it on open. Seal + nonce derivation come from
// the shared crypto package, so what this harness sends is what real receivers
// decrypt. When no key material is available it sends plaintext (matching the prior
// fallback), which only happens if the server issued a non-32-byte key.
func (b *bot) mediaPkt(typ, flags, codec uint8, ssrc uint32, seq uint16, ts uint32, ctr uint64, payload []byte) []byte {
	hdr := protocol.MediaHeader{
		Type:      typ,
		Flags:     flags,
		KeyID:     b.keyID,
		Codec:     codec,
		Sequence:  seq,
		Timestamp: ts,
		SSRC:      ssrc,
		Counter:   ctr,
	}
	aad := hdr.Marshal()

	enc := payload
	if b.sc != nil {
		enc = b.sc.EncryptSSRC(aad, payload, ctr, ssrc)
	}

	out := make([]byte, len(aad)+len(enc))
	copy(out, aad)
	copy(out[len(aad):], enc)
	return out
}
