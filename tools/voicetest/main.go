package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	authv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/auth/v1"
	callv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/call/v1"
	membershipv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/membership/v1"
	roomsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/rooms/v1"
	usersv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/users/v1"
	"golang.org/x/crypto/hkdf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const (
	mediaHeaderSize = 24
	nonceSize       = 12
	nonceBaseSize   = 4

	pktHello   = 0x01
	pktWelcome = 0x02
	pktAudio   = 0x03
	pktVideo   = 0x04
	pktPing    = 0x05
	pktPong    = 0x06
	pktBye     = 0x07

	codecOpus = 1
	codecH264 = 2
)

var (
	grpcAddr             = flag.String("grpc", envOr("GRPC_API_URL", "localhost:9090"), "gRPC API address")
	useTLS               = flag.Bool("tls", envOr("USE_TLS", "false") == "true", "TLS for gRPC")
	numClients           = flag.Int("clients", 3, "simulated clients")
	testDur              = flag.Duration("duration", 30*time.Second, "test duration")
	sendVideo            = flag.Bool("video", false, "send fake video packets")
	roomName             = flag.String("room", "stress-test-room", "room name")
	baseHandle           = flag.String("handle", "stressbot", "base handle")
	pw                   = flag.String("password", "testtest123", "bot password")
	audioRateMs          = flag.Int("audio-rate", 20, "audio interval ms")
	videoRateMs          = flag.Int("video-rate", 33, "video interval ms")
	rateLimitBypassToken = flag.String("rl-bypass-token", envOr("RATE_LIMIT_BYPASS_TOKEN", ""), "rate limit bypass token")
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type stats struct {
	audioSent  atomic.Uint64
	videoSent  atomic.Uint64
	audioRecv  atomic.Uint64
	videoRecv  atomic.Uint64
	pongRecv   atomic.Uint64
	welcomeOK  atomic.Uint64
	errors     atomic.Uint64
	bytesOut   atomic.Uint64
	bytesIn    atomic.Uint64
	rttSamples []time.Duration
	rttMu      sync.Mutex
}

func (s *stats) addRTT(d time.Duration) {
	s.rttMu.Lock()
	s.rttSamples = append(s.rttSamples, d)
	s.rttMu.Unlock()
}

func (s *stats) summary() string {
	s.rttMu.Lock()
	defer s.rttMu.Unlock()
	var avg, mn, mx time.Duration
	if n := len(s.rttSamples); n > 0 {
		mn = s.rttSamples[0]
		for _, r := range s.rttSamples {
			avg += r
			if r < mn {
				mn = r
			}
			if r > mx {
				mx = r
			}
		}
		avg /= time.Duration(n)
	}
	return fmt.Sprintf(
		"audio_tx=%d video_tx=%d audio_rx=%d video_rx=%d pongs=%d welcomes=%d errs=%d out=%dKB in=%dKB rtt(avg=%v min=%v max=%v n=%d)",
		s.audioSent.Load(), s.videoSent.Load(),
		s.audioRecv.Load(), s.videoRecv.Load(),
		s.pongRecv.Load(), s.welcomeOK.Load(), s.errors.Load(),
		s.bytesOut.Load()/1024, s.bytesIn.Load()/1024,
		avg, mn, mx, len(s.rttSamples),
	)
}

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
	conn        *net.UDPConn
	st          *stats
	ready       chan struct{}
}

func withRateLimitBypass(ctx context.Context) context.Context {
	if *rateLimitBypassToken == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "x-concord-ratelimit-bypass", *rateLimitBypassToken)
}

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	ctx, cancel := context.WithCancel(context.Background())
	rpcBaseCtx := withRateLimitBypass(ctx)
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; log.Println("shutting down..."); cancel() }()

	var dialOpt grpc.DialOption
	if *useTLS {
		dialOpt = grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(nil, ""))
	} else {
		dialOpt = grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	conn, err := grpc.NewClient(*grpcAddr, dialOpt)
	if err != nil {
		log.Fatalf("grpc dial: %v", err)
	}
	defer conn.Close()

	authC := authv1.NewAuthServiceClient(conn)
	usersC := usersv1.NewUsersServiceClient(conn)
	roomsC := roomsv1.NewRoomsServiceClient(conn)
	callC := callv1.NewCallServiceClient(conn)
	memberC := membershipv1.NewMembershipServiceClient(conn)

	st := &stats{}
	bots := make([]*bot, *numClients)

	for i := 0; i < *numClients; i++ {
		h := fmt.Sprintf("%s%d", *baseHandle, i)
		log.Printf("[SETUP] auth %s", h)
		tok, err := loginOrRegister(rpcBaseCtx, authC, h, *pw)
		if err != nil {
			log.Fatalf("auth %s: %v", h, err)
		}

		selfCtx := withAuth(rpcBaseCtx, tok)
		self, err := usersC.GetSelf(selfCtx, &usersv1.GetSelfRequest{})
		if err != nil {
			log.Fatalf("getSelf %s: %v", h, err)
		}

		bots[i] = &bot{
			idx:    i,
			handle: h,
			userID: self.Id,
			token:  tok,
			st:     st,
			ready:  make(chan struct{}),
		}
	}

	log.Printf("[SETUP] creating room %q", *roomName)
	ownerCtx := withAuth(rpcBaseCtx, bots[0].token)

	roomResp, err := roomsC.CreateRoom(ownerCtx, &roomsv1.CreateRoomRequest{
		Name:        *roomName,
		Description: "voice stress test",
	})
	if err != nil {
		log.Printf("[SETUP] create room failed (may exist): %v — listing", err)
		lr, err2 := roomsC.ListRoomsForUser(ownerCtx, &roomsv1.ListRoomsForUserRequest{})
		if err2 != nil {
			log.Fatalf("list rooms: %v", err2)
		}
		found := false
		for _, r := range lr.Rooms {
			if r.Name == *roomName {
				for _, b := range bots {
					b.roomID = r.Id
				}
				found = true
				break
			}
		}
		if !found {
			log.Fatalf("room %q not found", *roomName)
		}
	} else {
		for _, b := range bots {
			b.roomID = roomResp.Id
		}
	}
	log.Printf("[SETUP] room_id=%s", bots[0].roomID)

	for i := 1; i < *numClients; i++ {
		log.Printf("[SETUP] inviting bot %d (%s) to room", i, bots[i].userID)
		_, err := memberC.Invite(ownerCtx, &membershipv1.InviteRequest{
			RoomId: bots[0].roomID,
			UserId: bots[i].userID,
		})
		if err != nil {
			log.Printf("[SETUP] invite bot %d failed (may already be member): %v", i, err)
		} else {
			botCtx := withAuth(rpcBaseCtx, bots[i].token)
			invites, err := memberC.ListRoomInvites(botCtx, &membershipv1.ListRoomInvitesRequest{})
			if err == nil {
				for _, inv := range invites.Incoming {
					if inv.RoomId == bots[0].roomID {
						_, _ = memberC.AcceptRoomInvite(botCtx, &membershipv1.AcceptRoomInviteRequest{InviteId: inv.Id})
						break
					}
				}
			}
		}
	}

	for _, b := range bots {
		log.Printf("[SETUP] bot %s joining voice", b.handle)
		bCtx := withAuth(rpcBaseCtx, b.token)
		vr, err := callC.JoinVoice(bCtx, &callv1.JoinVoiceRequest{
			RoomId:    b.roomID,
			AudioOnly: !*sendVideo,
		})
		if err != nil {
			log.Fatalf("join voice %s: %v", b.handle, err)
		}
		b.voiceToken = vr.VoiceToken
		b.udpHost = vr.Endpoint.Host
		b.udpPort = int(vr.Endpoint.Port)
		b.keyMaterial = vr.Crypto.KeyMaterial
		if len(vr.Crypto.KeyId) > 0 {
			b.keyID = vr.Crypto.KeyId[0]
		}
		log.Printf("[SETUP] bot %s: endpoint=%s:%d participants=%d", b.handle, b.udpHost, b.udpPort, len(vr.Participants))
	}

	log.Printf("[TEST] starting %d bots for %v", *numClients, *testDur)

	testCtx, testCancel := context.WithTimeout(ctx, *testDur)
	defer testCancel()

	var wg sync.WaitGroup

	for _, b := range bots {
		if err := b.dial(); err != nil {
			log.Fatalf("dial %s: %v", b.handle, err)
		}
		if err := b.hello(); err != nil {
			log.Fatalf("hello %s: %v", b.handle, err)
		}

		wg.Add(1)
		go func(b *bot) { defer wg.Done(); b.recvLoop(testCtx) }(b)

		wg.Add(1)
		go func(b *bot) { defer wg.Done(); b.audioLoop(testCtx) }(b)

		if *sendVideo {
			wg.Add(1)
			go func(b *bot) { defer wg.Done(); b.vidLoop(testCtx) }(b)
		}

		wg.Add(1)
		go func(b *bot) { defer wg.Done(); b.pingLoop(testCtx) }(b)

		time.Sleep(100 * time.Millisecond)
	}

	tick := time.NewTicker(5 * time.Second)
	go func() {
		for {
			select {
			case <-tick.C:
				log.Printf("[STATS] %s", st.summary())
			case <-testCtx.Done():
				tick.Stop()
				return
			}
		}
	}()

	wg.Wait()

	for _, b := range bots {
		bye := make([]byte, 5)
		bye[0] = pktBye
		binary.BigEndian.PutUint32(bye[1:], b.ssrc)
		b.conn.Write(bye)
		b.conn.Close()
	}

	for _, b := range bots {
		bCtx := withAuth(rpcBaseCtx, b.token)
		callC.LeaveVoice(bCtx, &callv1.LeaveVoiceRequest{RoomId: b.roomID})
	}

	log.Println("========== FINAL ==========")
	log.Println(st.summary())

	if st.errors.Load() > 0 {
		os.Exit(1)
	}
}

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

func withAuth(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

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
	b.conn = c
	return nil
}

func (b *bot) hello() error {
	payload := map[string]interface{}{
		"token":         b.voiceToken,
		"protocol":      1,
		"codec":         "opus",
		"room_id":       b.roomID,
		"user_id":       b.userID,
		"video_enabled": *sendVideo,
		"video_codec":   "h264",
		"crypto": map[string]interface{}{
			"aead":   "aes-256-gcm",
			"key_id": []byte{b.keyID},
		},
	}
	js, _ := json.Marshal(payload)
	pkt := append([]byte{pktHello}, js...)
	_, err := b.conn.Write(pkt)
	return err
}

func (b *bot) recvLoop(ctx context.Context) {
	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		b.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, err := b.conn.Read(buf)
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
		case pktWelcome:
			var w map[string]interface{}
			if err := json.Unmarshal(buf[1:n], &w); err != nil {
				log.Printf("[BOT %d] bad welcome json: %v", b.idx, err)
				continue
			}
			if v, ok := w["ssrc"].(float64); ok {
				b.ssrc = uint32(v)
			}
			if v, ok := w["video_ssrc"].(float64); ok {
				b.videoSSRC = uint32(v)
			}
			if v, ok := w["screen_ssrc"].(float64); ok {
				b.screenSSRC = uint32(v)
			}
			b.st.welcomeOK.Add(1)
			log.Printf("[BOT %d] welcome ssrc=%d video=%d screen=%d", b.idx, b.ssrc, b.videoSSRC, b.screenSSRC)
			select {
			case <-b.ready:
			default:
				close(b.ready)
			}

		case pktAudio:
			b.st.audioRecv.Add(1)

		case pktVideo:
			b.st.videoRecv.Add(1)

		case pktPong:
			if n >= 9 {
				sent := int64(binary.BigEndian.Uint64(buf[1:9]))
				rtt := time.Duration(time.Now().UnixMilli()-sent) * time.Millisecond
				b.st.pongRecv.Add(1)
				b.st.addRTT(rtt)
			}
		}
	}
}

func (b *bot) audioLoop(ctx context.Context) {
	select {
	case <-b.ready:
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Second):
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
			pkt := b.mediaPkt(pktAudio, 0, codecOpus, b.ssrc, seq, ts, ctr, fakeOpus)
			if _, err := b.conn.Write(pkt); err != nil {
				b.st.errors.Add(1)
			} else {
				b.st.audioSent.Add(1)
				b.st.bytesOut.Add(uint64(len(pkt)))
			}
			seq++
			ctr++
			ts += 960
		}
	}
}

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
			var flags uint8
			if fc%90 == 0 {
				flags = 0x01
			}
			pkt := b.mediaPkt(pktVideo, flags, codecH264, b.videoSSRC, seq, ts, ctr, fake)
			if _, err := b.conn.Write(pkt); err != nil {
				b.st.errors.Add(1)
			} else {
				b.st.videoSent.Add(1)
				b.st.bytesOut.Add(uint64(len(pkt)))
			}
			seq++
			ctr++
			ts += 3000
			fc++
		}
	}
}

func (b *bot) pingLoop(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			pkt := make([]byte, 9)
			pkt[0] = pktPing
			binary.BigEndian.PutUint64(pkt[1:], uint64(time.Now().UnixMilli()))
			b.conn.Write(pkt)
		}
	}
}

func (b *bot) mediaPkt(typ, flags, codec uint8, ssrc uint32, seq uint16, ts uint32, ctr uint64, payload []byte) []byte {
	hdr := make([]byte, mediaHeaderSize)
	hdr[0] = typ
	hdr[1] = flags
	hdr[2] = b.keyID
	hdr[3] = codec
	binary.BigEndian.PutUint16(hdr[4:6], seq)
	binary.BigEndian.PutUint32(hdr[6:10], ts)
	binary.BigEndian.PutUint32(hdr[10:14], ssrc)
	binary.BigEndian.PutUint64(hdr[14:22], ctr)

	enc := b.seal(hdr, payload, ssrc, ctr)
	out := make([]byte, mediaHeaderSize+len(enc))
	copy(out, hdr)
	copy(out[mediaHeaderSize:], enc)
	return out
}

func (b *bot) seal(aad, plaintext []byte, ssrc uint32, counter uint64) []byte {
	if len(b.keyMaterial) != 32 {
		return plaintext
	}
	nonce := b.nonce(ssrc, counter)
	block, err := aes.NewCipher(b.keyMaterial)
	if err != nil {
		return plaintext
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return plaintext
	}
	return gcm.Seal(nil, nonce, plaintext, aad)
}

func (b *bot) nonce(ssrc uint32, counter uint64) []byte {
	nb := deriveNonceBase(b.keyMaterial, b.roomID, ssrc, b.keyID)
	n := make([]byte, nonceSize)
	copy(n[:nonceBaseSize], nb)
	binary.BigEndian.PutUint64(n[nonceBaseSize:], counter)
	return n
}

func deriveNonceBase(keyMaterial []byte, roomID string, ssrc uint32, keyID byte) []byte {
	info := []byte("nonce-base\x00")
	info = append(info, []byte(roomID)...)
	info = append(info, keyID)
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, ssrc)
	info = append(info, buf...)

	reader := hkdf.New(sha256.New, keyMaterial, nil, info)
	out := make([]byte, nonceBaseSize)
	io.ReadFull(reader, out)
	return out
}
