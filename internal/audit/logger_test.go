package audit

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// TestLogPersistsAndList verifies that Log writes an audit row that List reads back
// scoped to a room, newest-first, with metadata preserved — and that events for
// other rooms are excluded.
func TestLogPersistsAndList(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	al := NewLogger(pool, zap.NewNop())

	roomA := uuid.NewString()
	roomB := uuid.NewString()
	actor := uuid.NewString()

	// Two events in roomA, one in roomB.
	if err := al.Log(ctx, Event{
		RoomID: roomA, UserID: actor, Action: "user.kick",
		ResourceID: "target-1", ResourceType: "user",
	}); err != nil {
		t.Fatalf("Log 1: %v", err)
	}
	if err := al.Log(ctx, Event{
		RoomID: roomA, UserID: actor, Action: "user.ban",
		ResourceID: "target-2", ResourceType: "user",
		Metadata: map[string]interface{}{"duration": float64(3600)},
	}); err != nil {
		t.Fatalf("Log 2: %v", err)
	}
	if err := al.Log(ctx, Event{
		RoomID: roomB, UserID: actor, Action: "user.mute", ResourceID: "target-3",
	}); err != nil {
		t.Fatalf("Log 3: %v", err)
	}

	events, err := al.List(ctx, roomA, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events for roomA, got %d", len(events))
	}
	// Newest first: the ban was logged after the kick.
	if events[0].Action != "user.ban" {
		t.Errorf("expected newest event first (user.ban), got %q", events[0].Action)
	}
	if events[1].Action != "user.kick" {
		t.Errorf("expected user.kick second, got %q", events[1].Action)
	}
	// Metadata round-trips.
	if got := events[0].Metadata["duration"]; got != float64(3600) {
		t.Errorf("expected metadata duration=3600, got %v", got)
	}
	// Room scoping: roomB's event must not appear.
	for _, e := range events {
		if e.Action == "user.mute" {
			t.Errorf("roomB event leaked into roomA listing")
		}
	}

	// Assigned fields are populated.
	if events[0].ID == uuid.Nil {
		t.Error("expected event ID to be assigned")
	}
	if events[0].Timestamp.IsZero() {
		t.Error("expected event Timestamp to be assigned")
	}
}
