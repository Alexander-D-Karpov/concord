package push

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// fakeSender records messages and can report specific tokens as invalid.
type fakeSender struct {
	mu      sync.Mutex
	sent    []Message
	invalid map[string]bool
}

func (f *fakeSender) Send(_ context.Context, msgs []Message) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var bad []string
	for _, m := range msgs {
		f.sent = append(f.sent, m)
		if f.invalid[m.Token] {
			bad = append(bad, m.Token)
		}
	}
	return bad, nil
}
func (f *fakeSender) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

func TestDispatchSendsAndPrunesInvalidTokens(t *testing.T) {
	pool := testutil.Pool(t)
	repo := NewRepository(pool)
	ctx := context.Background()
	user := testutil.SeedUser(t, pool, "pd-"+uuid.NewString()[:8])
	_ = repo.Upsert(ctx, Device{UserID: user, DeviceID: "good", FCMToken: "tok-good"})
	_ = repo.Upsert(ctx, Device{UserID: user, DeviceID: "dead", FCMToken: "tok-dead"})

	fs := &fakeSender{invalid: map[string]bool{"tok-dead": true}}
	d := NewDispatcher(repo, fs, zap.NewNop())
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.Start(runCtx)

	d.DispatchChat(user, "conv-1", "msg-1", "sender-1")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fs.count() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if fs.count() != 2 {
		t.Fatalf("expected 2 messages sent (one per device), got %d", fs.count())
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := repo.ListByUser(ctx, user)
		if len(list) == 1 && list[0].FCMToken == "tok-good" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected the invalid token to be pruned, leaving only tok-good")
}

func TestBuildChatData(t *testing.T) {
	d := buildChatData("conv", "msg", "sndr")
	if d["type"] != "message" || d["conversation_id"] != "conv" || d["message_id"] != "msg" || d["sender_id"] != "sndr" {
		t.Errorf("unexpected chat data: %v", d)
	}
}
