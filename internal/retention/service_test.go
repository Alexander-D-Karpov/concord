package retention

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// TestPurgeOnceSoftDeletesExpired verifies the retention purge soft-deletes
// messages older than a room's retention window, leaves recent messages, and skips
// rooms with retention disabled (retention_days = 0).
func TestPurgeOnceSoftDeletesExpired(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	svc := NewService(pool, zap.NewNop())

	owner := testutil.SeedUser(t, pool, "owner-"+uuid.NewString()[:8])
	roomA := testutil.SeedRoom(t, pool, owner, 0) // retention 1 day
	roomB := testutil.SeedRoom(t, pool, owner, 0) // retention disabled

	mustExec(t, pool, `INSERT INTO room_settings (room_id, retention_days) VALUES ($1, 1)`, roomA)
	mustExec(t, pool, `INSERT INTO room_settings (room_id, retention_days) VALUES ($1, 0)`, roomB)

	// roomA: one old message (should purge), one recent (should stay).
	oldMsg := int64(1000001)
	newMsg := int64(1000002)
	mustExec(t, pool, `INSERT INTO messages (id, room_id, author_id, content, created_at) VALUES ($1,$2,$3,'old', NOW() - INTERVAL '3 days')`, oldMsg, roomA, owner)
	mustExec(t, pool, `INSERT INTO messages (id, room_id, author_id, content, created_at) VALUES ($1,$2,$3,'new', NOW())`, newMsg, roomA, owner)
	// roomB: old message but retention disabled (should stay).
	roomBMsg := int64(1000003)
	mustExec(t, pool, `INSERT INTO messages (id, room_id, author_id, content, created_at) VALUES ($1,$2,$3,'oldB', NOW() - INTERVAL '3 days')`, roomBMsg, roomB, owner)

	n, err := svc.PurgeOnce(ctx)
	if err != nil {
		t.Fatalf("PurgeOnce: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 message purged, got %d", n)
	}

	if !isDeleted(t, pool, oldMsg) {
		t.Error("expected old roomA message to be soft-deleted")
	}
	if isDeleted(t, pool, newMsg) {
		t.Error("expected recent roomA message to remain")
	}
	if isDeleted(t, pool, roomBMsg) {
		t.Error("expected roomB message to remain (retention disabled)")
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func isDeleted(t *testing.T, pool *pgxpool.Pool, id int64) bool {
	t.Helper()
	var deleted bool
	if err := pool.QueryRow(context.Background(),
		`SELECT deleted_at IS NOT NULL FROM messages WHERE id = $1`, id,
	).Scan(&deleted); err != nil {
		t.Fatalf("read message %d: %v", id, err)
	}
	return deleted
}
