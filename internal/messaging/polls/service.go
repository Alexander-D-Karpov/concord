package polls

import (
	"context"
	"fmt"
	"strconv"
	"time"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	featuresv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/features/v1"
	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// pollCacheTTL is how long a rendered poll proto is cached per (poll, user).
const pollCacheTTL = 30 * time.Second

// messageInserter is the seam through which the poll's backing chat/DM message is
// inserted inside the poll-creation transaction, keeping polls decoupled from the
// concrete message repositories.
type messageInserter interface {
	InsertRoomMessageTx(ctx context.Context, tx pgx.Tx, id int64, roomID, authorID uuid.UUID, content string, createdAt time.Time) error
	InsertDMMessageTx(ctx context.Context, tx pgx.Tx, id int64, channelID, authorID uuid.UUID, content string, createdAt time.Time) error
}

// Service implements poll creation, voting, and closing, and broadcasts the
// resulting message/vote events over the hub. It owns caching of rendered polls
// and a direct pool used only for the surface lookup during broadcast.
type Service struct {
	repo      *Repository
	msgs      messageInserter
	hub       *events.Hub
	snowflake *infra.SnowflakeGenerator
	cache     *cache.AsidePattern
	logger    *zap.Logger
	pool      *pgxpool.Pool
}

// NewService wires the poll Service. hub and cache may be nil, in which case
// broadcasts and caching are skipped.
func NewService(repo *Repository, msgs messageInserter, hub *events.Hub, sf *infra.SnowflakeGenerator, aside *cache.AsidePattern, pool *pgxpool.Pool, logger *zap.Logger) *Service {
	return &Service{repo: repo, msgs: msgs, hub: hub, snowflake: sf, cache: aside, pool: pool, logger: logger}
}

// pollKey builds the per-(poll, user) cache key, since a poll renders differently
// per user (their own votes are included).
func pollKey(pollID, userID uuid.UUID) string {
	return fmt.Sprintf("poll:%s:%s", pollID, userID)
}

// invalidatePoll drops all cached renders of a poll (across users) after a change.
// It is a no-op when caching is disabled.
func (s *Service) invalidatePoll(ctx context.Context, pollID uuid.UUID) {
	if s.cache == nil {
		return
	}
	_ = s.cache.DeletePattern(ctx, fmt.Sprintf("poll:%s:*", pollID))
}

// Create validates and creates a poll (2-10 options; exactly one of room_id or
// channel_id) and its backing message in a single transaction, generating a
// snowflake message ID. After commit it broadcasts a MessageCreated event and
// returns the rendered message. Returns Unauthorized/BadRequest on invalid input
// and Internal on DB failure.
func (s *Service) Create(ctx context.Context, req *featuresv1.CreatePollRequest) (*featuresv1.CreatePollResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("not authenticated")
	}
	if len(req.Options) < 2 || len(req.Options) > 10 {
		return nil, errors.BadRequest("2-10 options required")
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	var roomID, channelID *uuid.UUID
	if req.RoomId != "" {
		r, err := uuid.Parse(req.RoomId)
		if err != nil {
			return nil, errors.BadRequest("invalid room id")
		}
		roomID = &r
	}
	if req.ChannelId != "" {
		c, err := uuid.Parse(req.ChannelId)
		if err != nil {
			return nil, errors.BadRequest("invalid channel id")
		}
		channelID = &c
	}
	if roomID == nil && channelID == nil {
		return nil, errors.BadRequest("room_id or channel_id required")
	}

	msgID := s.snowflake.Generate()
	msgCreatedAt := s.snowflake.ExtractTimestamp(msgID)
	pollID := uuid.New()

	var closeDate *time.Time
	if req.CloseAfterSeconds > 0 {
		t := time.Now().Add(time.Duration(req.CloseAfterSeconds) * time.Second)
		closeDate = &t
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, errors.Internal("tx begin failed", err)
	}
	defer tx.Rollback(ctx)

	content := fmt.Sprintf("📊 %s", req.Question)
	if roomID != nil {
		if err := s.msgs.InsertRoomMessageTx(ctx, tx, msgID, *roomID, userUUID, content, msgCreatedAt); err != nil {
			return nil, errors.Internal("message insert failed", err)
		}
	} else {
		if err := s.msgs.InsertDMMessageTx(ctx, tx, msgID, *channelID, userUUID, content, msgCreatedAt); err != nil {
			return nil, errors.Internal("message insert failed", err)
		}
	}

	correctOpt := int(req.CorrectOption)
	p := &Poll{
		ID: pollID, MessageID: msgID, RoomID: roomID, ChannelID: channelID,
		CreatorID: userUUID, Question: req.Question, PollType: int(req.PollType),
		IsAnonymous: req.IsAnonymous, AllowsMultiple: req.AllowsMultiple,
		CorrectOption: &correctOpt, Explanation: &req.Explanation, CloseDate: closeDate,
	}
	if err := s.repo.InsertTx(ctx, tx, p); err != nil {
		return nil, errors.Internal("poll insert failed", err)
	}

	for i, text := range req.Options {
		if err := s.repo.InsertOptionTx(ctx, tx, pollID, i, text); err != nil {
			return nil, errors.Internal("option insert failed", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, errors.Internal("commit failed", err)
	}

	protoMsg := s.buildProtoMessage(msgID, userID, content, msgCreatedAt, roomID, channelID, pollID, req)
	s.broadcastMessageCreated(roomID, channelID, protoMsg)

	return &featuresv1.CreatePollResponse{Message: protoMsg}, nil
}

// buildProtoMessage assembles the commonv1.Message (with embedded Poll) broadcast
// on creation, before any votes exist. RoomId carries whichever scope applies.
func (s *Service) buildProtoMessage(msgID int64, userID, content string, createdAt time.Time, roomID, channelID *uuid.UUID, pollID uuid.UUID, req *featuresv1.CreatePollRequest) *commonv1.Message {
	protoOpts := make([]*commonv1.PollOption, len(req.Options))
	for i, t := range req.Options {
		protoOpts[i] = &commonv1.PollOption{OptionId: int32(i), Text: t}
	}

	msg := &commonv1.Message{
		Id: strconv.FormatInt(msgID, 10), AuthorId: userID, Content: content,
		CreatedAt: timestamppb.New(createdAt),
		Poll: &commonv1.Poll{
			Id: pollID.String(), Question: req.Question, Options: protoOpts,
			PollType: req.PollType, IsAnonymous: req.IsAnonymous,
			AllowsMultiple: req.AllowsMultiple, CorrectOption: req.CorrectOption,
			Explanation: req.Explanation,
		},
	}
	if roomID != nil {
		msg.RoomId = roomID.String()
	} else if channelID != nil {
		msg.RoomId = channelID.String()
	}
	if req.CloseAfterSeconds > 0 {
		t := time.Now().Add(time.Duration(req.CloseAfterSeconds) * time.Second)
		msg.Poll.CloseDate = timestamppb.New(t)
	}
	return msg
}

// broadcastMessageCreated emits a MessageCreated event to the poll's room or DM
// channel. It is a no-op when the hub is nil.
func (s *Service) broadcastMessageCreated(roomID, channelID *uuid.UUID, msg *commonv1.Message) {
	if s.hub == nil {
		return
	}
	ev := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_MessageCreated{
			MessageCreated: &streamv1.MessageCreated{Message: msg},
		},
	}
	if roomID != nil {
		s.hub.BroadcastToRoom(roomID.String(), ev)
	} else if channelID != nil {
		s.hub.BroadcastToRoom(channelID.String(), ev)
	}
}

// Vote records a user's vote(s) in a transaction: for single-choice polls it
// first clears prior votes, inserts each chosen option, then recalculates counts.
// It rejects votes on a closed poll, invalidates the cache, broadcasts a
// PollVoteUpdated event, and returns the freshly rendered poll.
func (s *Service) Vote(ctx context.Context, req *featuresv1.VotePollRequest) (*featuresv1.VotePollResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("not authenticated")
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}
	pollUUID, err := uuid.Parse(req.PollId)
	if err != nil {
		return nil, errors.BadRequest("invalid poll id")
	}

	isClosed, allowsMultiple, err := s.repo.GetFlags(ctx, pollUUID)
	if err != nil {
		return nil, errors.NotFound("poll not found")
	}
	if isClosed {
		return nil, errors.BadRequest("poll is closed")
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, errors.Internal("tx failed", err)
	}
	defer tx.Rollback(ctx)

	if !allowsMultiple {
		if err := s.repo.DeleteUserVotesTx(ctx, tx, pollUUID, userUUID); err != nil {
			return nil, errors.Internal("clear votes failed", err)
		}
	}
	for _, optID := range req.OptionIds {
		if err := s.repo.InsertVoteTx(ctx, tx, pollUUID, userUUID, optID); err != nil {
			return nil, errors.Internal("vote failed", err)
		}
	}
	if err := s.repo.RecalcCountsTx(ctx, tx, pollUUID); err != nil {
		return nil, errors.Internal("recount failed", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, errors.Internal("commit failed", err)
	}

	s.invalidatePoll(ctx, pollUUID)

	poll := s.loadProto(ctx, pollUUID, userUUID)
	s.broadcastVoteUpdate(ctx, pollUUID, poll)

	return &featuresv1.VotePollResponse{Poll: poll}, nil
}

// ClosePoll closes a poll on behalf of its creator (ownership is enforced in the
// repository) and invalidates the cache. It returns an empty response even if the
// caller does not own the poll, since the underlying update simply affects no rows.
func (s *Service) ClosePoll(ctx context.Context, req *featuresv1.ClosePollRequest) (*emptypb.Empty, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("not authenticated")
	}
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}
	pollUUID, err := uuid.Parse(req.PollId)
	if err != nil {
		return nil, errors.BadRequest("invalid poll id")
	}
	if err := s.repo.Close(ctx, pollUUID, userUUID); err != nil {
		return nil, errors.Internal("close failed", err)
	}
	s.invalidatePoll(ctx, pollUUID)
	return &emptypb.Empty{}, nil
}

// loadProto returns a poll rendered for one user (including their own votes),
// serving from cache when available and otherwise loading from the repository and
// caching the result for pollCacheTTL. It returns nil if the poll cannot be loaded.
func (s *Service) loadProto(ctx context.Context, pollID, userID uuid.UUID) *commonv1.Poll {
	if s.cache != nil {
		var cached commonv1.Poll
		if err := s.cache.Get(ctx, pollKey(pollID, userID), &cached); err == nil {
			return &cached
		}
	}

	row, options, myVotes, err := s.repo.Load(ctx, pollID, userID)
	if err != nil {
		return nil
	}

	protoOpts := make([]*commonv1.PollOption, len(options))
	for i, o := range options {
		protoOpts[i] = &commonv1.PollOption{
			OptionId: int32(o.OptionID), Text: o.Text, VoteCount: int32(o.VoteCount),
		}
	}
	myVoteStrs := make([]string, len(myVotes))
	for i, v := range myVotes {
		myVoteStrs[i] = strconv.Itoa(v)
	}

	poll := &commonv1.Poll{
		Id: pollID.String(), Question: row.Question, Options: protoOpts,
		PollType: int32(row.PollType), IsAnonymous: row.IsAnonymous,
		AllowsMultiple: row.AllowsMultiple, IsClosed: row.IsClosed,
		TotalVoters: int32(row.TotalVoters), MyVotes: myVoteStrs,
	}
	if row.CorrectOption != nil {
		poll.CorrectOption = int32(*row.CorrectOption)
	}
	if row.Explanation != nil {
		poll.Explanation = *row.Explanation
	}
	if row.CloseDate != nil {
		poll.CloseDate = timestamppb.New(*row.CloseDate)
	}

	if s.cache != nil {
		_ = s.cache.Set(ctx, pollKey(pollID, userID), poll, pollCacheTTL)
	}
	return poll
}

// broadcastVoteUpdate looks up the poll's surface (room/channel) directly from the
// DB and emits a PollVoteUpdated event there. It is a no-op when the hub is nil or
// the surface lookup fails.
func (s *Service) broadcastVoteUpdate(ctx context.Context, pollID uuid.UUID, poll *commonv1.Poll) {
	if s.hub == nil {
		return
	}

	var roomID, channelID *uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT room_id, channel_id FROM polls WHERE id = $1`, pollID,
	).Scan(&roomID, &channelID)
	if err != nil {
		return
	}

	ev := &streamv1.ServerEvent{
		EventId:   uuid.New().String(),
		CreatedAt: timestamppb.Now(),
		Payload: &streamv1.ServerEvent_PollVoteUpdated{
			PollVoteUpdated: &streamv1.PollVoteUpdated{
				PollId: pollID.String(), Poll: poll,
			},
		},
	}

	if roomID != nil {
		s.hub.BroadcastToRoom(roomID.String(), ev)
	} else if channelID != nil {
		s.hub.BroadcastToRoom(channelID.String(), ev)
	}
}

// RunCloser is a blocking background loop that closes expired polls every 30s
// until ctx is cancelled. Run it in its own goroutine.
func (s *Service) RunCloser(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := s.repo.CloseExpired(ctx); err == nil && n > 0 {
				s.logger.Info("closed expired polls", zap.Int64("count", n))
			}
		}
	}
}
