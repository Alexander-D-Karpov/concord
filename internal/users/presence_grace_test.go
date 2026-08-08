package users

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// newBarePresence builds a PresenceManager with only the maps populated, so the
// pure grace helpers can be exercised without a DB or hub.
func newBarePresence() *PresenceManager {
	return &PresenceManager{
		currentState:   map[uuid.UUID]string{},
		pendingOffline: map[uuid.UUID]time.Time{},
	}
}

func TestGraceMarkExpireAndClear(t *testing.T) {
	pm := newBarePresence()
	u := uuid.New()
	base := time.Now()

	pm.markDisconnectedAt(u, base)
	if pm.currentState[u] != StatusAway {
		t.Fatalf("expected away, got %q", pm.currentState[u])
	}
	if got := pm.expiredGrace(base.Add(30*time.Second), time.Minute); len(got) != 0 {
		t.Errorf("expected none expired before grace, got %v", got)
	}
	got := pm.expiredGrace(base.Add(90*time.Second), time.Minute)
	if len(got) != 1 || got[0] != u {
		t.Errorf("expected %s expired after grace, got %v", u, got)
	}
	pm.markDisconnectedAt(u, base)
	pm.clearPending(u)
	if got := pm.expiredGrace(base.Add(90*time.Second), time.Minute); len(got) != 0 {
		t.Errorf("expected cleared user not to expire, got %v", got)
	}
}
