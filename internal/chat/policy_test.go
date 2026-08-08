package chat

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/testutil"
	"github.com/google/uuid"
)

// newPolicyTestService builds a minimal chat.Service (no hub/slowmode/mentions) for
// exercising SendMessage's room-policy enforcement against a live database.
func newPolicyTestService(t *testing.T) (*Service, *rooms.Repository) {
	t.Helper()
	pool := testutil.Pool(t)
	repo := NewRepository(pool, infra.NewSnowflakeGenerator(1))
	roomsRepo := rooms.NewRepository(pool)
	svc := NewService(repo, roomsRepo, nil, nil, nil, nil, nil, nil)
	return svc, roomsRepo
}

// TestSendMessageWhoCanPost verifies that when who_can_post is "moderator", a plain
// member is refused but a moderator/admin can still post.
func TestSendMessageWhoCanPost(t *testing.T) {
	svc, roomsRepo := newPolicyTestService(t)
	pool := testutil.Pool(t)
	ctx := context.Background()

	owner := testutil.SeedUser(t, pool, "owner-"+uuid.NewString()[:8])
	member := testutil.SeedUser(t, pool, "member-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, owner, 0)
	testutil.SeedMembership(t, pool, room, owner, "admin")
	testutil.SeedMembership(t, pool, room, member, "member")

	// Restrict posting to moderators+.
	s, err := roomsRepo.GetSettings(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	s.WhoCanPost = "moderator"
	if err := roomsRepo.UpdateSettings(ctx, room, s); err != nil {
		t.Fatal(err)
	}

	// Plain member is refused.
	memberCtx := interceptor.ContextWithAuth(ctx, member.String(), "member", nil)
	if _, err := svc.SendMessage(memberCtx, SendMessageParams{RoomID: room.String(), Content: "hi"}); err == nil {
		t.Fatal("expected member to be refused when who_can_post=moderator")
	}

	// Admin can post.
	adminCtx := interceptor.ContextWithAuth(ctx, owner.String(), "owner", nil)
	if _, err := svc.SendMessage(adminCtx, SendMessageParams{RoomID: room.String(), Content: "hello"}); err != nil {
		t.Fatalf("expected admin to be allowed to post, got %v", err)
	}
}

// TestSendMessageWordFilterCensor verifies filtered words are masked (whole-word,
// case-insensitive) in the stored message content, while substrings are untouched.
func TestSendMessageWordFilterCensor(t *testing.T) {
	svc, roomsRepo := newPolicyTestService(t)
	pool := testutil.Pool(t)
	ctx := context.Background()

	owner := testutil.SeedUser(t, pool, "owner-"+uuid.NewString()[:8])
	room := testutil.SeedRoom(t, pool, owner, 0)
	testutil.SeedMembership(t, pool, room, owner, "admin")

	s, _ := roomsRepo.GetSettings(ctx, room)
	s.WordFilters = []string{"badword"}
	if err := roomsRepo.UpdateSettings(ctx, room, s); err != nil {
		t.Fatal(err)
	}

	adminCtx := interceptor.ContextWithAuth(ctx, owner.String(), "owner", nil)
	msg, err := svc.SendMessage(adminCtx, SendMessageParams{
		RoomID:  room.String(),
		Content: "this BadWord and badword but not badwordy",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// Both whole-word occurrences masked (case-insensitive); the substring "badwordy" is left alone.
	want := "this *** and *** but not badwordy"
	if msg.Content != want {
		t.Errorf("expected censored content %q, got %q", want, msg.Content)
	}

	// Editing must also censor — otherwise the filter is trivially bypassed.
	edited, err := svc.EditMessage(adminCtx, room.String(), msg.ID, "sneaky badword edit")
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if edited.Content != "sneaky *** edit" {
		t.Errorf("expected edit to be censored, got %q", edited.Content)
	}
}

// TestCensorWordsUnicode covers non-ASCII (Cyrillic) whole-word filtering, since the
// deployment's default region is Russian-speaking.
func TestCensorWordsUnicode(t *testing.T) {
	got := censorWords("это плохое слово и плохоеы", []string{"плохое"})
	want := "это *** слово и плохоеы"
	if got != want {
		t.Errorf("censorWords unicode: got %q, want %q", got, want)
	}
	// Adjacent filtered words separated by a single space are both masked.
	got2 := censorWords("bad bad ok", []string{"bad"})
	if got2 != "*** *** ok" {
		t.Errorf("censorWords adjacent: got %q, want %q", got2, "*** *** ok")
	}
}
