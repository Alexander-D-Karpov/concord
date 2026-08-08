package call

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeMemberChecker stands in for the rooms repository so the voice-access gate can
// be exercised without a database, and records whether the membership check ran.
type fakeMemberChecker struct {
	member bool
	banned bool
	calls  int
}

func (f *fakeMemberChecker) IsMember(ctx context.Context, roomID, userID uuid.UUID) (bool, error) {
	f.calls++
	return f.member, nil
}

func (f *fakeMemberChecker) IsBanned(ctx context.Context, roomID, userID uuid.UUID) (bool, error) {
	return f.banned, nil
}

func authedCtx(userID string) context.Context {
	return interceptor.ContextWithAuth(context.Background(), userID, "handle", nil)
}

func newTestHandler(debug, member bool) (*Handler, *fakeMemberChecker) {
	fake := &fakeMemberChecker{member: member}
	return &Handler{roomsRepo: fake, logger: zap.NewNop(), debug: debug}, fake
}

// Prod safety: with VOICE_DEBUG off, a non-member must be rejected. This is the gate
// that stops arbitrary users mass-joining voice and DoSing the media plane.
func TestVoiceAccess_NonMemberRejectedWhenNotDebug(t *testing.T) {
	h, fake := newTestHandler(false, false)
	_, err := h.requireVoiceAccess(authedCtx(uuid.NewString()), uuid.NewString())
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-member must be forbidden, got code=%v err=%v", status.Code(err), err)
	}
	if fake.calls != 1 {
		t.Fatalf("membership check must run when not debug, calls=%d", fake.calls)
	}
}

// With VOICE_DEBUG on, a non-member is admitted and the membership DB check is
// skipped entirely — the harness fast-join path.
func TestVoiceAccess_NonMemberAdmittedWhenDebug(t *testing.T) {
	h, fake := newTestHandler(true, false)
	uid := uuid.NewString()
	got, err := h.requireVoiceAccess(authedCtx(uid), uuid.NewString())
	if err != nil {
		t.Fatalf("debug must admit non-member: %v", err)
	}
	if got != uid {
		t.Fatalf("want userID %q, got %q", uid, got)
	}
	if fake.calls != 0 {
		t.Fatalf("debug must skip the membership DB check, calls=%d", fake.calls)
	}
}

// Members are always admitted with the flag off (normal production path).
func TestVoiceAccess_MemberAdmittedWhenNotDebug(t *testing.T) {
	h, _ := newTestHandler(false, true)
	uid := uuid.NewString()
	got, err := h.requireVoiceAccess(authedCtx(uid), uuid.NewString())
	if err != nil || got != uid {
		t.Fatalf("member must be admitted: got=%q err=%v", got, err)
	}
}

// A banned user is refused voice access even if they are still a room member — the
// ban is enforced at the voice gate, not just at invite acceptance.
func TestVoiceAccess_BannedMemberRejected(t *testing.T) {
	h, fake := newTestHandler(false, true)
	fake.banned = true
	_, err := h.requireVoiceAccess(authedCtx(uuid.NewString()), uuid.NewString())
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("banned member must be forbidden, got code=%v err=%v", status.Code(err), err)
	}
}

// Authentication is still required even in debug mode — an unauthenticated caller is
// rejected before any room logic.
func TestVoiceAccess_UnauthenticatedRejectedEvenInDebug(t *testing.T) {
	h, _ := newTestHandler(true, false)
	_, err := h.requireVoiceAccess(context.Background(), uuid.NewString())
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated must be rejected even in debug, got code=%v", status.Code(err))
	}
}
