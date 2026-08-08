package dm

import (
	"context"
	"sync"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/Alexander-D-Karpov/concord/internal/messaging/editing"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/Alexander-D-Karpov/concord/internal/voiceassign"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// recordedCallPush captures a single fakeCallPusher.PushCall invocation for
// assertions.
type recordedCallPush struct {
	userID     uuid.UUID
	callID     string
	roomOrDMID string
	callerID   string
}

// fakeCallPusher is a MessagePusher test double that records PushCall
// invocations without touching any real push infrastructure.
type fakeCallPusher struct {
	mu    sync.Mutex
	calls []recordedCallPush
}

func (f *fakeCallPusher) PushDMMessage(ctx context.Context, userID, channelID uuid.UUID, messageID int64, senderID uuid.UUID) {
}

func (f *fakeCallPusher) PushCall(userID uuid.UUID, callID, roomOrDMID, callerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCallPush{userID: userID, callID: callID, roomOrDMID: roomOrDMID, callerID: callerID})
}

// seedVoiceServer inserts a live, online voice server so voiceassign has
// something to place the call on.
func seedVoiceServer(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(), `
		INSERT INTO voice_servers (name, region, addr_udp, addr_ctrl, status, capacity_hint, load_score, updated_at)
		VALUES ('test-server', 'test', '127.0.0.1:50000', '127.0.0.1:50001', 'online', 1000, 0, NOW())
		RETURNING id
	`).Scan(&id)
	if err != nil {
		t.Fatalf("seed voice server: %v", err)
	}
	return id
}

// newCallPushTestService builds a real, DB-backed dm.Service (mirroring
// cmd/concord-api/main.go's wiring) with a fakeCallPusher installed, for
// exercising StartCall's incoming-call push hook end to end.
func newCallPushTestService(t *testing.T) (*Service, *fakeCallPusher, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.Pool(t)

	repo := NewRepository(pool)
	msgRepo := NewMessageRepository(pool, infra.NewSnowflakeGenerator(1), editing.NewRecorder())
	usersRepo := users.NewRepository(pool)
	hub := events.NewHub(zap.NewNop(), pool, nil)
	jwtManager := jwt.NewManager("test-secret", "test-voice-secret")
	voiceAssign := voiceassign.NewService(pool, jwtManager, nil, nil, hub)

	svc := NewService(repo, msgRepo, usersRepo, hub, voiceAssign, nil, nil, nil, nil, zap.NewNop())

	pusher := &fakeCallPusher{}
	svc.SetPusher(pusher)

	return svc, pusher, pool
}

// TestStartCallPushesCallee verifies that StartCall pushes an incoming-call
// notification to the OTHER DM participant (the callee), never to the caller.
func TestStartCallPushesCallee(t *testing.T) {
	svc, pusher, pool := newCallPushTestService(t)
	ctx := context.Background()

	caller := testutil.SeedUser(t, pool, "caller-"+uuid.NewString()[:8])
	callee := testutil.SeedUser(t, pool, "callee-"+uuid.NewString()[:8])
	channel := testutil.SeedDMChannel(t, pool, caller, callee)
	seedVoiceServer(t, pool)

	callerCtx := interceptor.ContextWithAuth(ctx, caller.String(), "caller", nil)

	_, callID, err := svc.StartCall(callerCtx, channel.String(), true)
	if err != nil {
		t.Fatalf("StartCall failed: %v", err)
	}
	if callID == "" {
		t.Fatal("expected a non-empty call id")
	}

	pusher.mu.Lock()
	defer pusher.mu.Unlock()

	if len(pusher.calls) != 1 {
		t.Fatalf("expected exactly 1 PushCall invocation, got %d: %+v", len(pusher.calls), pusher.calls)
	}

	got := pusher.calls[0]
	if got.userID != callee {
		t.Errorf("PushCall userID = %s, want callee %s", got.userID, callee)
	}
	if got.userID == caller {
		t.Errorf("PushCall must not target the caller")
	}
	if got.callID != callID {
		t.Errorf("PushCall callID = %q, want %q (StartCall's returned call id)", got.callID, callID)
	}
	if got.roomOrDMID != channel.String() {
		t.Errorf("PushCall roomOrDMID = %q, want channel id %q", got.roomOrDMID, channel.String())
	}
	if got.callerID != caller.String() {
		t.Errorf("PushCall callerID = %q, want caller id %q", got.callerID, caller.String())
	}
}
