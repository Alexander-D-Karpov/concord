package rooms

import (
	"context"
	"sync"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

// TestAddMemberIfBelowCapConcurrent proves the cap is enforced atomically: with a
// cap of 3 and 10 users joining concurrently, exactly 3 succeed and the final
// member count is exactly 3 (no TOCTOU overshoot).
func TestAddMemberIfBelowCapConcurrent(t *testing.T) {
	pool := testutil.Pool(t)
	repo := NewRepository(pool)
	ctx := context.Background()

	owner := testutil.SeedUser(t, pool, "owner-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, owner, 0)

	const cap = 3
	const n = 10
	users := make([]uuid.UUID, n)
	for i := range users {
		users[i] = testutil.SeedUser(t, pool, "u-"+uuid.NewString()[:8])
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	added := 0
	for _, u := range users {
		wg.Add(1)
		go func(uid uuid.UUID) {
			defer wg.Done()
			ok, err := repo.AddMemberIfBelowCap(ctx, room, uid, "member", cap)
			if err != nil {
				t.Errorf("AddMemberIfBelowCap: %v", err)
				return
			}
			if ok {
				mu.Lock()
				added++
				mu.Unlock()
			}
		}(u)
	}
	wg.Wait()

	if added != cap {
		t.Errorf("expected exactly %d successful joins, got %d", cap, added)
	}
	count, err := repo.CountMembers(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	if count != cap {
		t.Errorf("expected %d members after concurrent joins, got %d", cap, count)
	}
}
