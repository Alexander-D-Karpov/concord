package push

import (
	"context"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/features"
	"github.com/google/uuid"
)

// MuteChecker reports whether a user has muted a room or channel for notifications.
type MuteChecker interface {
	IsMuted(ctx context.Context, userID uuid.UUID, roomID, channelID *uuid.UUID) (bool, error)
}

// featuresMuteChecker implements MuteChecker over the features notification-override store.
type featuresMuteChecker struct{ repo *features.Repository }

// NewMuteChecker builds a MuteChecker backed by the features repository.
func NewMuteChecker(repo *features.Repository) MuteChecker { return &featuresMuteChecker{repo: repo} }

func (m *featuresMuteChecker) IsMuted(ctx context.Context, userID uuid.UUID, roomID, channelID *uuid.UUID) (bool, error) {
	overrides, err := m.repo.ListNotificationOverrides(ctx, userID)
	if err != nil {
		return false, err
	}
	now := time.Now()
	for _, o := range overrides {
		match := (roomID != nil && o.RoomID != nil && *o.RoomID == *roomID) ||
			(channelID != nil && o.ChannelID != nil && *o.ChannelID == *channelID)
		if !match {
			continue
		}
		if o.MuteUntil != nil && o.MuteUntil.After(now) {
			return true, nil
		}
		if isMutedLevel(o.OverrideLevel) {
			return true, nil
		}
	}
	return false, nil
}

// isMutedLevel reports whether an override level means "do not notify". The
// notification_overrides.override_level column is constrained by a DB CHECK to
// exactly ('all','mentions','nothing','default') — see
// internal/infra/migrations/013_feature_parity.sql. Of those, "nothing" is the
// only value that unambiguously means silence, so it is the only one treated as
// muted by this generic chat/DM push hook; "all" and "default" both want normal
// notifications. Note: "mentions" is NOT treated as muted here, which means a
// user on "mentions" currently still receives a push for every message via this
// hook (there is no mention-filtered push path yet — mention delivery today is a
// WebSocket stream event, not a push). Narrowing "mentions" to mention-only push
// delivery is future work, not in scope for this generic new-message hook.
func isMutedLevel(level string) bool {
	switch level {
	case "nothing":
		return true
	}
	return false
}
