package admin

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

type fakeEvictor struct {
	rotated []string
	left    [][2]string
}

func (f *fakeEvictor) RotateRoomKey(ctx context.Context, roomID string) error {
	f.rotated = append(f.rotated, roomID)
	return nil
}

func (f *fakeEvictor) LeaveVoice(ctx context.Context, roomID, userID string) error {
	f.left = append(f.left, [2]string{roomID, userID})
	return nil
}

// TestBanEvictsLiveVoice verifies that banning (and kicking) a user triggers voice
// isolation: their session placement is cleared (LeaveVoice) and the room key is
// rotated (RotateRoomKey). This asserts the calls fire; the resulting media-plane
// isolation follows from the rotation mechanism (see VoiceEvictor docs) and is not
// exercised against a live voice server here.
func TestBanEvictsLiveVoice(t *testing.T) {
	svc, _, _, room, admin := newAdminTestService(t)
	fake := &fakeEvictor{}
	svc.SetVoiceEvictor(fake)
	ctx := context.Background()

	target := testutil.SeedUser(t, testutil.Pool(t), "target-"+uuid.NewString()[:8])
	if err := svc.BanUser(ctx, admin.String(), room.String(), target.String(), 0); err != nil {
		t.Fatalf("BanUser: %v", err)
	}

	if len(fake.rotated) == 0 || fake.rotated[0] != room.String() {
		t.Errorf("expected room key rotated on ban, rotated=%v", fake.rotated)
	}
	found := false
	for _, l := range fake.left {
		if l[0] == room.String() && l[1] == target.String() {
			found = true
		}
	}
	if !found {
		t.Errorf("expected target's voice session cleared on ban, left=%v", fake.left)
	}

	// Kick should evict too.
	fake2 := &fakeEvictor{}
	svc.SetVoiceEvictor(fake2)
	testutil.SeedMembership(t, testutil.Pool(t), room, target, "member")
	if err := svc.KickUser(ctx, admin.String(), room.String(), target.String()); err != nil {
		t.Fatalf("KickUser: %v", err)
	}
	if len(fake2.rotated) == 0 {
		t.Errorf("expected room key rotated on kick, rotated=%v", fake2.rotated)
	}
}
