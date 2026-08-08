package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	authv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/auth/v1"
	callv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/call/v1"
	membershipv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/membership/v1"
	roomsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/rooms/v1"
	usersv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/users/v1"
	"github.com/Alexander-D-Karpov/concord/internal/voice/crypto"
	"github.com/Alexander-D-Karpov/concord/internal/voice/protocol"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/joho/godotenv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Load the repo .env before the flag vars below are initialized, so env-based
// defaults (notably RATE_LIMIT_BYPASS_TOKEN, needed to get past the auth limiter
// when registering many bots) are populated. Declared first so it runs first; a
// missing .env is fine (Load errors are ignored). Run the tool from the repo root.
var _ = godotenv.Load(".env")

// The bot client, its media loops, and the aggregate stats live in client.go and
// stats.go; the synthetic media, renderers, and TUI in signal.go / render.go /
// mediatap.go / tui.go. This file is just flags, run-mode selection, and orchestration.
// Packet types, codecs, header sizes and crypto primitives come from the backend's
// internal/voice/{protocol,crypto} packages so this harness speaks the exact
// production wire format — no duplicated constants that can silently drift.

// Command-line flags for the harness: gRPC endpoint/TLS and auth, load shape
// (client count, duration, audio/video rates, video toggle), network impairment
// (loss/jitter/reorder), churn/netchange intervals, advertised capabilities
// (FEC/DTX/max-bitrate/report-loss), and run mode (fast-join, CI, TUI, render-dump).
// Several defaults come from env vars via envOr.
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
	lossRate             = flag.Float64("loss", 0, "outgoing packet loss probability (0..1)")
	jitterMaxMs          = flag.Int("jitter", 0, "max added jitter ms on outgoing packets")
	reorderRate          = flag.Float64("reorder", 0, "outgoing packet reorder probability (0..1)")
	churnEvery           = flag.Duration("churn", 0, "if >0, bots leave+rejoin (bye/hello) at this interval")
	netChangeEvery       = flag.Duration("netchange", 0, "if >0, bots rebind their UDP socket at this interval (exercises migration)")
	strictCI             = flag.Bool("ci", false, "strict assertions + non-zero exit on failure")
	fec                  = flag.Bool("fec", false, "advertise FEC capability in Hello")
	dtx                  = flag.Bool("dtx", false, "advertise DTX capability in Hello")
	maxBitrate           = flag.Int("max-bitrate", 2_000_000, "advertised max_bitrate (bps)")
	reportLoss           = flag.Float64("report-loss", 0, "loss fraction to report in RR (drives server BitrateHint)")
	fastJoin             = flag.Bool("fast-join", true, "skip room invites and join voice directly (requires server VOICE_DEBUG=true)")
	tuiMode              = flag.String("tui", "auto", "live TUI: auto|on|off (auto = on when stdout is a TTY and not -ci)")
	renderDump           = flag.String("render-dump", "", "headless: periodically write the monitor's rendered media panel to this file")
)

// envOr returns environment variable key, or def when it is unset or empty. Used to
// seed flag defaults from the environment.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// wantTUI decides whether to run the interactive dashboard. "auto" enables it only
// when stdout is a real terminal and we're not in CI mode (which is headless).
func wantTUI() bool {
	switch *tuiMode {
	case "on":
		return !*strictCI
	case "off":
		return false
	default:
		return !*strictCI && isTTY(os.Stdout)
	}
}

// isTTY reports whether f is a character device (an interactive terminal) rather than
// a pipe or regular file.
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// monTap returns the monitor bot's media tap, or nil if there is no monitor, letting
// callers pass an absent monitor through safely.
func monTap(b *bot) *mediaTap {
	if b == nil {
		return nil
	}
	return b.tap
}

// statsLogLoop prints the aggregate summary every 5s (plain-log / headless mode).
func statsLogLoop(ctx context.Context, st *stats) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			log.Printf("[STATS] %s", st.summary())
		case <-ctx.Done():
			return
		}
	}
}

// renderDumpLoop periodically writes the monitor's rendered media panel to a file so
// the end-to-end media path can be inspected without an interactive terminal.
func renderDumpLoop(ctx context.Context, path string, tap *mediaTap, cfg tuiConfig) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if err := dumpRender(path, tap.snapshot(), cfg); err != nil {
				log.Printf("render-dump: %v", err)
			}
		case <-ctx.Done():
			_ = dumpRender(path, tap.snapshot(), cfg)
			return
		}
	}
}

// main is the harness orchestrator. It parses flags, dials the gRPC API, and for each
// simulated client authenticates and fetches its user ID. It creates (or reuses) the
// stress room, then either fast-joins voice directly (requires server VOICE_DEBUG) or
// runs the real invite/accept membership flow, and calls JoinVoice to obtain each bot's
// voice token, UDP endpoint and room key. It selects a run mode (interactive TUI,
// headless render-dump, or plain-log), optionally designates a publisher/monitor media
// demo, dials each bot's UDP socket and launches its recv/audio/video/ping/churn/
// netchange/RR goroutines. After the test duration it sends Byes, leaves voice, prints
// the final stats, and in -ci mode asserts end-to-end health, exiting non-zero on
// failure (or on any transport error).
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

	// fast-join relies on the server's VOICE_DEBUG gate to skip the membership check,
	// so bots can JoinVoice directly without the invite/accept round-trips. Without a
	// debug server, run with -fast-join=false to exercise the real membership path.
	if *fastJoin {
		log.Printf("[SETUP] fast-join: skipping invites, bots join voice directly (needs server VOICE_DEBUG=true)")
	} else {
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
	}

	for _, b := range bots {
		log.Printf("[SETUP] bot %s joining voice", b.handle)
		bCtx := withAuth(rpcBaseCtx, b.token)
		vr, err := callC.JoinVoice(bCtx, &callv1.JoinVoiceRequest{
			RoomId:    b.roomID,
			AudioOnly: !*sendVideo,
		})
		if err != nil {
			if *fastJoin && status.Code(err) == codes.PermissionDenied {
				log.Fatalf("join voice %s: %v\n"+
					"  → fast-join requires the API to run in debug mode: go run ./cmd/concord-api -debug=true\n"+
					"    (or VOICE_DEBUG=true). Against a normal API, re-run this tool with -fast-join=false.",
					b.handle, err)
			}
			log.Fatalf("join voice %s: %v", b.handle, err)
		}
		b.voiceToken = vr.VoiceToken
		b.udpHost = vr.Endpoint.Host
		b.udpPort = int(vr.Endpoint.Port)
		b.keyMaterial = vr.Crypto.KeyMaterial
		if len(vr.Crypto.KeyId) > 0 {
			b.keyID = vr.Crypto.KeyId[0]
		}
		if len(b.keyMaterial) == crypto.KeySize {
			sc, serr := crypto.NewSessionCryptoDerived(b.keyMaterial, b.roomID, b.keyID)
			if serr != nil {
				log.Fatalf("session crypto %s: %v", b.handle, serr)
			}
			b.sc = sc
		}
		log.Printf("[SETUP] bot %s: endpoint=%s:%d participants=%d", b.handle, b.udpHost, b.udpPort, len(vr.Participants))
	}

	testCtx, testCancel := context.WithTimeout(ctx, *testDur)
	defer testCancel()

	var logFile *os.File

	// Run mode: TUI (interactive) vs plain-log (CI/headless). The media demo — a
	// publisher emitting a real tone + animated video and a monitor decrypting and
	// rendering it — runs whenever there is somewhere to render or assert it.
	useTUI := wantTUI()
	if useTUI {
		// Route logs to a file so they don't corrupt the TUI's alt-screen.
		if f, err := os.Create("voicetest.log"); err == nil {
			logFile = f
			log.SetOutput(f)
		}
	}

	mediaDemo := *numClients >= 2 && (useTUI || *renderDump != "" || *strictCI)
	var pub, mon *bot
	if mediaDemo {
		pub, mon = bots[0], bots[1]
		pub.role, pub.tone = rolePublisher, &toneSource{}
		if *sendVideo {
			pub.video = &videoSource{}
		}
		mon.role, mon.tap = roleMonitor, &mediaTap{}
		if c, err := crypto.NewCipher(mon.keyMaterial); err == nil {
			mon.rxCipher = c
		}
		log.Printf("[MEDIA] publisher=bot%d monitor=bot%d video=%v", pub.idx, mon.idx, *sendVideo)
	}

	var prog *tea.Program
	if useTUI {
		cfg := tuiConfig{clients: *numClients, video: *sendVideo, fastJoin: *fastJoin, duration: *testDur}
		if mediaDemo {
			cfg.publisherIdx, cfg.monitorIdx = pub.idx, mon.idx
		}
		prog = tea.NewProgram(newTUIModel(st, monTap(mon), cfg), tea.WithAltScreen())
	}

	log.Printf("[TEST] starting %d bots for %v", *numClients, *testDur)

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

		wg.Add(1)
		go func(b *bot) { defer wg.Done(); b.churnLoop(testCtx) }(b)
		wg.Add(1)
		go func(b *bot) { defer wg.Done(); b.netchangeLoop(testCtx) }(b)
		wg.Add(1)
		go func(b *bot) { defer wg.Done(); b.rrLoop(testCtx) }(b)

		time.Sleep(100 * time.Millisecond)
	}

	// Once the publisher is welcomed, point the monitor at its SSRCs. Race-free: the
	// monitor's tap reads these atomics and they stay 0 until this store.
	if mediaDemo {
		go func() {
			select {
			case <-pub.ready:
				mon.watchAudioSSRC.Store(pub.ssrc)
				mon.watchVideoSSRC.Store(pub.videoSSRC)
				log.Printf("[MEDIA] monitor watching publisher audio_ssrc=%d video_ssrc=%d", pub.ssrc, pub.videoSSRC)
			case <-testCtx.Done():
			}
		}()
	}

	switch {
	case useTUI:
		go func() { <-testCtx.Done(); prog.Quit() }()
		go func() {
			if _, err := prog.Run(); err != nil {
				log.Printf("tui error: %v", err)
			}
			testCancel() // quitting the TUI ends the run
		}()
	case *renderDump != "" && mon != nil:
		go renderDumpLoop(testCtx, *renderDump, mon.tap, tuiConfig{
			clients: *numClients, video: *sendVideo, fastJoin: *fastJoin,
			duration: *testDur, publisherIdx: pub.idx, monitorIdx: mon.idx,
		})
		go statsLogLoop(testCtx, st)
	default:
		go statsLogLoop(testCtx, st)
	}

	wg.Wait()

	if useTUI {
		if prog != nil {
			prog.Quit()
		}
		time.Sleep(80 * time.Millisecond) // let the alt-screen restore
		log.SetOutput(os.Stderr)
		if logFile != nil {
			_ = logFile.Close()
		}
	}

	for _, b := range bots {
		bye := make([]byte, 5)
		bye[0] = protocol.PacketTypeBye
		binary.BigEndian.PutUint32(bye[1:], b.ssrc)
		b.getConn().Write(bye)
		b.getConn().Close()
	}

	for _, b := range bots {
		bCtx := withAuth(rpcBaseCtx, b.token)
		callC.LeaveVoice(bCtx, &callv1.LeaveVoiceRequest{RoomId: b.roomID})
	}

	log.Println("========== FINAL ==========")
	log.Println(st.summary())

	if *strictCI {
		failed := false
		fail := func(format string, args ...interface{}) {
			failed = true
			log.Printf("[CI-FAIL] "+format, args...)
		}
		if st.welcomeOK.Load() < uint64(*numClients) {
			fail("welcomes=%d < clients=%d (not all bots joined)", st.welcomeOK.Load(), *numClients)
		}
		if *numClients > 1 && st.audioRecv.Load() == 0 {
			fail("audio_rx=0 (no media relayed between %d bots)", *numClients)
		}
		if *sendVideo && *numClients > 1 && st.videoRecv.Load() == 0 {
			fail("video_rx=0 (no video relayed between %d bots)", *numClients)
		}
		// End-to-end content integrity: the monitor must actually DECRYPT and DECODE
		// the publisher's real media, not just receive bytes.
		if mon != nil && mon.tap != nil {
			snap := mon.tap.snapshot()
			if snap.audioFrames == 0 {
				fail("monitor decoded 0 audio frames from publisher (media pipeline broken)")
			}
			if *sendVideo && snap.videoFrames == 0 {
				fail("monitor decoded 0 video frames from publisher (video pipeline broken)")
			}
		}
		if st.errors.Load() > 0 {
			fail("errors=%d", st.errors.Load())
		}
		if st.pongRecv.Load() > 0 {
			st.rttMu.Lock()
			var avg time.Duration
			if n := len(st.rttSamples); n > 0 {
				for _, r := range st.rttSamples {
					avg += r
				}
				avg /= time.Duration(n)
			}
			st.rttMu.Unlock()
			if avg > 500*time.Millisecond {
				fail("avg RTT %v exceeds 500ms", avg)
			}
		}
		if *reportLoss > 0.05 && st.bitrateHints.Load() == 0 {
			fail("reported %.0f%% loss but received no BitrateHint (feedback loop broken)", *reportLoss*100)
		}
		if failed {
			log.Println("========== CI: FAIL ==========")
			os.Exit(1)
		}
		log.Println("========== CI: PASS ==========")
	}

	if st.errors.Load() > 0 {
		os.Exit(1)
	}
}
