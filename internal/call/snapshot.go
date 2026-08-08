package call

import (
	"context"
	"time"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/voiceassign"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// snapshotScopeLimit caps how many rooms and DM channels a single snapshot
	// scans, bounding the work done per reconnect.
	snapshotScopeLimit = 200
	// snapshotTimeout bounds the total time spent building one snapshot; scopes
	// not yet processed when it fires are dropped.
	snapshotTimeout = 5 * time.Second
)

// scope identifies one voice-capable container to inspect: a room, or a DM
// channel when isDM is set (which selects channel_id vs room_id in the emitted
// state).
type scope struct {
	id   string
	isDM bool
}

// Snapshotter pushes a user's full voice state across all their rooms and active
// DM calls after a reconnect, so a client that missed live events can resync.
type Snapshotter struct {
	voiceAssign *voiceassign.Service
	pool        *pgxpool.Pool
	hub         *events.Hub
	logger      *zap.Logger
}

// NewSnapshotter constructs a Snapshotter. It queries membership and DM tables
// directly through pool rather than going via a repository.
func NewSnapshotter(va *voiceassign.Service, pool *pgxpool.Pool, hub *events.Hub, logger *zap.Logger) *Snapshotter {
	return &Snapshotter{
		voiceAssign: va,
		pool:        pool,
		hub:         hub,
		logger:      logger,
	}
}

// SendSnapshot gathers the voice participants of every room and active DM call
// the user belongs to and broadcasts a single VoiceStateSnapshot to that user.
// Scopes with no participants are skipped, SelfConnected marks scopes the user
// is already in, and the whole operation is bounded by snapshotTimeout. It
// reports failures via logging only — there is no error return.
func (s *Snapshotter) SendSnapshot(ctx context.Context, userID string) {
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	scopes, err := s.userScopes(ctx, userUUID)
	if err != nil {
		s.logger.Warn("voice snapshot: failed to load user scopes",
			zap.String("user_id", userID), zap.Error(err))
		return
	}

	states := make([]*streamv1.RoomVoiceState, 0, 4)
	for _, sc := range scopes {
		select {
		case <-ctx.Done():
			s.logger.Warn("voice snapshot timed out", zap.String("user_id", userID))
			return
		default:
		}

		participants, err := s.voiceAssign.GetVoiceParticipants(ctx, sc.id)
		if err != nil {
			s.logger.Debug("voice snapshot: participants lookup failed",
				zap.String("scope_id", sc.id), zap.Error(err))
			continue
		}
		if len(participants) == 0 {
			continue
		}

		state := &streamv1.RoomVoiceState{}
		if sc.isDM {
			state.ChannelId = sc.id
		} else {
			state.RoomId = sc.id
		}
		for _, p := range participants {
			if p.UserID == userID {
				state.SelfConnected = true
			}
			state.Participants = append(state.Participants, ToParticipantState(p))
		}
		states = append(states, state)
	}

	s.hub.BroadcastToUser(userID, &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_VoiceStateSnapshot{
			VoiceStateSnapshot: &streamv1.VoiceStateSnapshot{Rooms: states},
		},
	})

	s.logger.Info("voice snapshot sent",
		zap.String("user_id", userID),
		zap.Int("scopes_scanned", len(scopes)),
		zap.Int("active_voice_scopes", len(states)),
	)
}

// userScopes lists the rooms the user is a member of plus the DM channels that
// currently have an active (not-yet-ended) call, each capped at
// snapshotScopeLimit. A failure loading DM channels is non-fatal: the
// already-collected room scopes are returned with a nil error.
func (s *Snapshotter) userScopes(ctx context.Context, userID uuid.UUID) ([]scope, error) {
	scopes := make([]scope, 0, 16)

	rows, err := s.pool.Query(ctx,
		`SELECT room_id FROM memberships WHERE user_id = $1 LIMIT $2`,
		userID, snapshotScopeLimit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			continue
		}
		scopes = append(scopes, scope{id: id.String()})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	dmRows, err := s.pool.Query(ctx,
		`SELECT dc.id FROM dm_channels dc
		 WHERE (dc.user1_id = $1 OR dc.user2_id = $1)
		   AND EXISTS (SELECT 1 FROM dm_calls WHERE channel_id = dc.id AND ended_at IS NULL)
		 LIMIT $2`,
		userID, snapshotScopeLimit)
	if err != nil {
		return scopes, nil
	}
	defer dmRows.Close()
	for dmRows.Next() {
		var id uuid.UUID
		if err := dmRows.Scan(&id); err != nil {
			continue
		}
		scopes = append(scopes, scope{id: id.String(), isDM: true})
	}

	return scopes, nil
}

// ToParticipantState maps a voiceassign participant to its proto state,
// returning nil for a nil input. JoinedAt is emitted only when positive,
// converting stored Unix seconds to a proto timestamp.
func ToParticipantState(p *voiceassign.VoiceParticipant) *streamv1.VoiceParticipantState {
	if p == nil {
		return nil
	}
	st := &streamv1.VoiceParticipantState{
		UserId:        p.UserID,
		Muted:         p.Muted,
		VideoEnabled:  p.VideoEnabled,
		ScreenSharing: p.ScreenSharing,
		Speaking:      p.Speaking,
	}
	if p.JoinedAt > 0 {
		st.JoinedAt = timestamppb.New(time.Unix(p.JoinedAt, 0))
	}
	return st
}
