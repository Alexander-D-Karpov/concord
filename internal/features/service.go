package features

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
	"github.com/Alexander-D-Karpov/concord/internal/features/gifprovider"
	"github.com/Alexander-D-Karpov/concord/internal/infra"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// scheduledMaxAttempts is how many delivery attempts a scheduled message gets
	// before it is marked permanently failed.
	scheduledMaxAttempts = 3
	// scheduledStuckTimeout is how long a message may sit in 'processing' before the
	// recovery sweep reclaims it as stuck.
	scheduledStuckTimeout = 2 * time.Minute
)

// Service implements the FeaturesService gRPC API and owns the scheduled-message
// delivery loop. gifProvider may be a disabled provider (see SearchGif) and hub
// may be nil, in which case broadcasts are skipped.
type Service struct {
	featuresv1.UnimplementedFeaturesServiceServer
	repo        *Repository
	hub         *events.Hub
	snowflake   *infra.SnowflakeGenerator
	logger      *zap.Logger
	gifProvider gifprovider.Provider
}

// NewService constructs the features Service from its dependencies.
func NewService(repo *Repository, hub *events.Hub, sf *infra.SnowflakeGenerator, logger *zap.Logger, gif gifprovider.Provider) *Service {
	return &Service{repo: repo, hub: hub, snowflake: sf, logger: logger, gifProvider: gif}
}

// ForwardMessages copies each source message into the destination room or channel,
// assigning fresh snowflake IDs and preserving forward attribution unless
// DropAuthor is set. Individual messages that can't be parsed, loaded, or inserted
// are skipped (not fatal); successfully forwarded room messages are broadcast. It
// requires authentication and a destination.
func (s *Service) ForwardMessages(ctx context.Context, req *featuresv1.ForwardMessagesRequest) (*featuresv1.ForwardMessagesResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	if req.DestinationRoomId == "" && req.DestinationChannelId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("destination required"))
	}

	callerUUID, _ := uuid.Parse(userID)
	var forwarded []*commonv1.Message

	for _, msgIDStr := range req.MessageIds {
		msgID, err := strconv.ParseInt(msgIDStr, 10, 64)
		if err != nil {
			continue
		}

		var src *ForwardSource
		var sourceRoomID, sourceChannelID *uuid.UUID

		if req.SourceRoomId != "" {
			rid, _ := uuid.Parse(req.SourceRoomId)
			sourceRoomID = &rid
			src, err = s.repo.GetRoomMessage(ctx, msgID, rid)
		} else if req.SourceChannelId != "" {
			cid, _ := uuid.Parse(req.SourceChannelId)
			sourceChannelID = &cid
			src, err = s.repo.GetDMMessage(ctx, msgID, cid)
		}
		if err != nil || src == nil {
			continue
		}

		authorName := s.repo.GetUserDisplayName(ctx, src.AuthorID)

		newID := s.snowflake.Generate()
		newCreatedAt := s.snowflake.ExtractTimestamp(newID)

		var fwdUserID *uuid.UUID
		var fwdUserName *string
		if !req.DropAuthor {
			fwdUserID = &src.AuthorID
			fwdUserName = &authorName
		}

		if req.DestinationRoomId != "" {
			drid, _ := uuid.Parse(req.DestinationRoomId)
			err = s.repo.InsertForwardedRoomMessage(ctx, newID, drid, callerUUID, src.Content, newCreatedAt,
				fwdUserID, fwdUserName, sourceRoomID, msgID, src.CreatedAt)
		} else {
			dcid, _ := uuid.Parse(req.DestinationChannelId)
			err = s.repo.InsertForwardedDMMessage(ctx, newID, dcid, callerUUID, src.Content, newCreatedAt,
				fwdUserID, fwdUserName, sourceChannelID, msgID, src.CreatedAt)
		}
		if err != nil {
			s.logger.Warn("forward insert failed", zap.Error(err))
			continue
		}

		protoMsg := &commonv1.Message{
			Id: strconv.FormatInt(newID, 10), RoomId: req.DestinationRoomId,
			AuthorId: userID, Content: src.Content, CreatedAt: timestamppb.New(newCreatedAt),
		}
		if !req.DropAuthor {
			protoMsg.ForwardInfo = &commonv1.ForwardInfo{
				OriginalAuthorId: src.AuthorID.String(), OriginalAuthorName: authorName,
				OriginalMessageId: msgIDStr, OriginalTimestamp: timestamppb.New(src.CreatedAt),
			}
			if sourceRoomID != nil {
				protoMsg.ForwardInfo.OriginalRoomId = sourceRoomID.String()
			}
			if sourceChannelID != nil {
				protoMsg.ForwardInfo.OriginalChannelId = sourceChannelID.String()
			}
		}
		forwarded = append(forwarded, protoMsg)

		if req.DestinationRoomId != "" && s.hub != nil {
			s.hub.BroadcastToRoom(req.DestinationRoomId, &streamv1.ServerEvent{
				EventId: uuid.New().String(), CreatedAt: timestamppb.Now(),
				Payload: &streamv1.ServerEvent_MessageCreated{MessageCreated: &streamv1.MessageCreated{Message: protoMsg}},
			})
		}
	}

	return &featuresv1.ForwardMessagesResponse{Messages: forwarded}, nil
}

// SearchGif proxies a GIF search to the configured provider. It returns
// FailedPrecondition when no provider is configured or it is disabled (empty API
// key), BadRequest for an empty query, and Unavailable if the provider call fails.
// limit is clamped to 1..50 (default 20).
func (s *Service) SearchGif(ctx context.Context, req *featuresv1.SearchGifRequest) (*featuresv1.SearchGifResponse, error) {
	if s.gifProvider == nil || !s.gifProvider.Enabled() {
		return nil, status.Error(codes.FailedPrecondition, "gif provider not configured")
	}
	if req.Query == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("query is required"))
	}

	limit := int(req.Limit)
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	page, err := s.gifProvider.Search(ctx, req.Query, limit, req.Offset)
	if err != nil {
		s.logger.Warn("gif search failed", zap.Error(err))
		return nil, status.Error(codes.Unavailable, "gif search failed")
	}

	results := make([]*featuresv1.GifResult, 0, len(page.Results))
	for _, r := range page.Results {
		results = append(results, &featuresv1.GifResult{
			Id: r.ID, Title: r.Title, Url: r.URL, PreviewUrl: r.PreviewURL,
			Width: int32(r.Width), Height: int32(r.Height),
		})
	}
	return &featuresv1.SearchGifResponse{Results: results, NextOffset: page.NextOffset}, nil
}

// ScheduleMessage persists a message for future delivery. It rejects times less
// than 30 seconds in the future and requires authentication; the returned message
// is 'pending'.
func (s *Service) ScheduleMessage(ctx context.Context, req *featuresv1.ScheduleMessageRequest) (*commonv1.ScheduledMessage, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}

	scheduledFor := req.ScheduledFor.AsTime()
	if scheduledFor.Before(time.Now().Add(30 * time.Second)) {
		return nil, errors.ToGRPCError(errors.BadRequest("must be at least 30 seconds in the future"))
	}

	userUUID, _ := uuid.Parse(userID)
	roomID, channelID := parseOptionalUUIDs(req.RoomId, req.ChannelId)

	var replyToID *int64
	if req.ReplyToId != "" {
		v, _ := strconv.ParseInt(req.ReplyToId, 10, 64)
		replyToID = &v
	}

	id, createdAt, err := s.repo.InsertScheduledMessage(ctx, roomID, channelID, userUUID, req.Content, replyToID, scheduledFor)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("failed to schedule", err))
	}

	return &commonv1.ScheduledMessage{
		Id: strconv.FormatInt(id, 10), RoomId: req.RoomId, ChannelId: req.ChannelId,
		AuthorId: userID, Content: req.Content, ReplyToId: req.ReplyToId,
		ScheduledFor: req.ScheduledFor, Status: "pending", CreatedAt: timestamppb.New(createdAt),
	}, nil
}

// GetScheduledMessages lists the caller's pending scheduled messages, optionally
// scoped to a room or channel. Requires authentication.
func (s *Service) GetScheduledMessages(ctx context.Context, req *featuresv1.GetScheduledMessagesRequest) (*featuresv1.GetScheduledMessagesResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	userUUID, _ := uuid.Parse(userID)
	roomID, channelID := parseOptionalUUIDs(req.RoomId, req.ChannelId)

	rows, err := s.repo.ListScheduledMessages(ctx, userUUID, roomID, channelID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("query failed", err))
	}

	var msgs []*commonv1.ScheduledMessage
	for _, row := range rows {
		sm := &commonv1.ScheduledMessage{
			Id: strconv.FormatInt(row.ID, 10), AuthorId: userID, Content: row.Content,
			ScheduledFor: timestamppb.New(row.ScheduledFor), Status: row.Status, CreatedAt: timestamppb.New(row.CreatedAt),
		}
		if row.RoomID != nil {
			sm.RoomId = row.RoomID.String()
		}
		if row.ChannelID != nil {
			sm.ChannelId = row.ChannelID.String()
		}
		if row.ReplyToID != nil {
			sm.ReplyToId = strconv.FormatInt(*row.ReplyToID, 10)
		}
		msgs = append(msgs, sm)
	}
	return &featuresv1.GetScheduledMessagesResponse{Messages: msgs}, nil
}

// EditScheduledMessage updates the content and time of the caller's pending
// scheduled message. Requires authentication; the update silently affects nothing
// if the message is not pending or not owned by the caller.
func (s *Service) EditScheduledMessage(ctx context.Context, req *featuresv1.EditScheduledMessageRequest) (*commonv1.ScheduledMessage, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	id, _ := strconv.ParseInt(req.Id, 10, 64)
	if err := s.repo.UpdateScheduledMessage(ctx, id, userID, req.Content, req.ScheduledFor.AsTime()); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("update failed", err))
	}
	return &commonv1.ScheduledMessage{Id: req.Id, Content: req.Content, ScheduledFor: req.ScheduledFor, Status: "pending"}, nil
}

// CancelScheduledMessage cancels the caller's pending scheduled message. Requires
// authentication; a no-op if not pending or not owned by the caller.
func (s *Service) CancelScheduledMessage(ctx context.Context, req *featuresv1.CancelScheduledMessageRequest) (*emptypb.Empty, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	id, _ := strconv.ParseInt(req.Id, 10, 64)
	if err := s.repo.CancelScheduledMessage(ctx, id, userID); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("cancel failed", err))
	}
	return &emptypb.Empty{}, nil
}

// SaveBookmark creates or updates the caller's bookmark on a message (note/tags
// overwrite on repeat). Requires authentication.
func (s *Service) SaveBookmark(ctx context.Context, req *featuresv1.SaveBookmarkRequest) (*commonv1.Bookmark, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	userUUID, _ := uuid.Parse(userID)
	msgID, _ := strconv.ParseInt(req.MessageId, 10, 64)
	roomID, channelID := parseOptionalUUIDs(req.RoomId, req.ChannelId)

	bmID, createdAt, err := s.repo.UpsertBookmark(ctx, userUUID, msgID, roomID, channelID, req.Note, req.Tags)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("bookmark failed", err))
	}
	return &commonv1.Bookmark{
		Id: bmID.String(), MessageId: req.MessageId, RoomId: req.RoomId,
		ChannelId: req.ChannelId, Note: req.Note, Tags: req.Tags,
		CreatedAt: timestamppb.New(createdAt),
	}, nil
}

// UnsaveBookmark removes the caller's bookmark on a message. Requires
// authentication; a no-op if none exists.
func (s *Service) UnsaveBookmark(ctx context.Context, req *featuresv1.UnsaveBookmarkRequest) (*emptypb.Empty, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	userUUID, _ := uuid.Parse(userID)
	msgID, _ := strconv.ParseInt(req.MessageId, 10, 64)
	if err := s.repo.DeleteBookmark(ctx, userUUID, msgID); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("delete failed", err))
	}
	return &emptypb.Empty{}, nil
}

// GetBookmarks returns the caller's bookmarks, optionally filtered by tag, with
// limit clamped to 1..100 (default 50). It over-fetches by one to compute HasMore
// for pagination. Requires authentication.
func (s *Service) GetBookmarks(ctx context.Context, req *featuresv1.GetBookmarksRequest) (*featuresv1.GetBookmarksResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	userUUID, _ := uuid.Parse(userID)
	limit := int(req.Limit)
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	rows, err := s.repo.ListBookmarks(ctx, userUUID, req.Tags, limit+1)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("query failed", err))
	}

	var bookmarks []*commonv1.Bookmark
	for _, row := range rows {
		bm := &commonv1.Bookmark{
			Id: row.ID.String(), MessageId: strconv.FormatInt(row.MessageID, 10),
			Note: row.Note, Tags: row.Tags, CreatedAt: timestamppb.New(row.CreatedAt),
		}
		if row.RoomID != nil {
			bm.RoomId = row.RoomID.String()
		}
		if row.ChannelID != nil {
			bm.ChannelId = row.ChannelID.String()
		}
		bookmarks = append(bookmarks, bm)
	}

	hasMore := len(bookmarks) > limit
	if hasMore {
		bookmarks = bookmarks[:limit]
	}
	return &featuresv1.GetBookmarksResponse{Bookmarks: bookmarks, HasMore: hasMore}, nil
}

// GetEditHistory returns a message's prior versions, newest first. Requires
// authentication.
func (s *Service) GetEditHistory(ctx context.Context, req *featuresv1.GetEditHistoryRequest) (*featuresv1.GetEditHistoryResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	msgID, _ := strconv.ParseInt(req.MessageId, 10, 64)

	rows, err := s.repo.ListEditHistory(ctx, msgID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("query failed", err))
	}

	var entries []*commonv1.EditHistoryEntry
	for _, row := range rows {
		entries = append(entries, &commonv1.EditHistoryEntry{
			Id: row.ID.String(), PreviousContent: row.PreviousContent,
			EditedAt: timestamppb.New(row.EditedAt), Version: int32(row.Version),
		})
	}
	return &featuresv1.GetEditHistoryResponse{Entries: entries}, nil
}

// CreatePoll atomically creates a poll and its carrier message in one transaction:
// it inserts the room/DM message, the poll header, and each option, then commits.
// Options must number 2..10. A CloseAfterSeconds>0 sets an auto-close time. On
// success the carrier message is broadcast to the room. Requires authentication.
//
// Note: this is the base implementation; the Aggregator overrides CreatePoll to
// delegate to the newer polls service.
func (s *Service) CreatePoll(ctx context.Context, req *featuresv1.CreatePollRequest) (*featuresv1.CreatePollResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	if len(req.Options) < 2 || len(req.Options) > 10 {
		return nil, errors.ToGRPCError(errors.BadRequest("2-10 options required"))
	}

	userUUID, _ := uuid.Parse(userID)
	roomID, channelID := parseOptionalUUIDs(req.RoomId, req.ChannelId)
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
		return nil, errors.ToGRPCError(errors.Internal("tx begin failed", err))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	content := fmt.Sprintf("📊 %s", req.Question)
	if roomID != nil {
		err = s.repo.InsertRoomMessageTx(ctx, tx, msgID, *roomID, userUUID, content, msgCreatedAt)
	} else if channelID != nil {
		err = s.repo.InsertDMMessageTx(ctx, tx, msgID, *channelID, userUUID, content, msgCreatedAt)
	}
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("message insert failed", err))
	}

	err = s.repo.InsertPollTx(ctx, tx, pollID, msgID, roomID, channelID, userUUID,
		req.Question, int(req.PollType), req.IsAnonymous, req.AllowsMultiple,
		int(req.CorrectOption), req.Explanation, closeDate)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("poll insert failed", err))
	}

	for i, optText := range req.Options {
		if err = s.repo.InsertPollOptionTx(ctx, tx, pollID, i, optText); err != nil {
			return nil, errors.ToGRPCError(errors.Internal("option insert failed", err))
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("commit failed", err))
	}

	protoOpts := make([]*commonv1.PollOption, len(req.Options))
	for i, t := range req.Options {
		protoOpts[i] = &commonv1.PollOption{OptionId: int32(i), Text: t}
	}

	protoMsg := &commonv1.Message{
		Id: strconv.FormatInt(msgID, 10), AuthorId: userID, Content: content,
		CreatedAt: timestamppb.New(msgCreatedAt),
		Poll: &commonv1.Poll{
			Id: pollID.String(), Question: req.Question, Options: protoOpts,
			PollType: req.PollType, IsAnonymous: req.IsAnonymous, AllowsMultiple: req.AllowsMultiple,
			CorrectOption: req.CorrectOption, Explanation: req.Explanation,
		},
	}
	if roomID != nil {
		protoMsg.RoomId = roomID.String()
	}
	if closeDate != nil {
		protoMsg.Poll.CloseDate = timestamppb.New(*closeDate)
	}

	if roomID != nil && s.hub != nil {
		s.hub.BroadcastToRoom(roomID.String(), &streamv1.ServerEvent{
			EventId: uuid.New().String(), CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MessageCreated{MessageCreated: &streamv1.MessageCreated{Message: protoMsg}},
		})
	}

	return &featuresv1.CreatePollResponse{Message: protoMsg}, nil
}

// VotePoll records the caller's vote(s) in a transaction, replacing prior votes
// first on single-choice polls, then recalculates tallies and returns the updated
// poll. Rejects votes on a closed poll (BadRequest) or a missing poll (NotFound).
// Requires authentication.
func (s *Service) VotePoll(ctx context.Context, req *featuresv1.VotePollRequest) (*featuresv1.VotePollResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	userUUID, _ := uuid.Parse(userID)
	pollUUID, _ := uuid.Parse(req.PollId)

	isClosed, allowsMultiple, err := s.repo.GetPollFlags(ctx, pollUUID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.NotFound("poll not found"))
	}
	if isClosed {
		return nil, errors.ToGRPCError(errors.BadRequest("poll is closed"))
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("tx failed", err))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if !allowsMultiple {
		_ = s.repo.DeleteUserPollVotes(ctx, tx, pollUUID, userUUID)
	}

	for _, optID := range req.OptionIds {
		if err := s.repo.InsertPollVoteTx(ctx, tx, pollUUID, userUUID, optID); err != nil {
			return nil, errors.ToGRPCError(errors.Internal("vote failed", err))
		}
	}

	if err := s.repo.RecalcPollCountsTx(ctx, tx, pollUUID); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("recount failed", err))
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("commit failed", err))
	}

	poll := s.loadProtoPoll(ctx, pollUUID, userUUID)
	return &featuresv1.VotePollResponse{Poll: poll}, nil
}

// ClosePoll closes a poll; the repository enforces that only the creator can close
// it, so a non-creator call is a silent no-op. Requires authentication.
func (s *Service) ClosePoll(ctx context.Context, req *featuresv1.ClosePollRequest) (*emptypb.Empty, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	pollUUID, _ := uuid.Parse(req.PollId)
	if err := s.repo.ClosePoll(ctx, pollUUID, userID); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("close failed", err))
	}
	return &emptypb.Empty{}, nil
}

// SaveDraft saves or replaces the caller's draft for a room/channel. Requires
// authentication.
func (s *Service) SaveDraft(ctx context.Context, req *featuresv1.SaveDraftRequest) (*commonv1.Draft, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	userUUID, _ := uuid.Parse(userID)
	roomID, channelID := parseOptionalUUIDs(req.RoomId, req.ChannelId)

	var replyToID *int64
	if req.ReplyToMessageId != "" {
		v, _ := strconv.ParseInt(req.ReplyToMessageId, 10, 64)
		replyToID = &v
	}

	if err := s.repo.UpsertDraft(ctx, userUUID, roomID, channelID, req.Content, replyToID); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("save draft failed", err))
	}
	return &commonv1.Draft{
		RoomId: req.RoomId, ChannelId: req.ChannelId,
		Content: req.Content, ReplyToMessageId: req.ReplyToMessageId,
		UpdatedAt: timestamppb.Now(),
	}, nil
}

// GetDrafts returns the caller's non-empty drafts, newest-updated first. Requires
// authentication.
func (s *Service) GetDrafts(ctx context.Context, req *featuresv1.GetDraftsRequest) (*featuresv1.GetDraftsResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	userUUID, _ := uuid.Parse(userID)

	rows, err := s.repo.ListDrafts(ctx, userUUID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("query failed", err))
	}

	var drafts []*commonv1.Draft
	for _, row := range rows {
		d := &commonv1.Draft{Content: row.Content, UpdatedAt: timestamppb.New(row.UpdatedAt)}
		if row.RoomID != nil {
			d.RoomId = row.RoomID.String()
		}
		if row.ChannelID != nil {
			d.ChannelId = row.ChannelID.String()
		}
		if row.ReplyToID != nil {
			d.ReplyToMessageId = strconv.FormatInt(*row.ReplyToID, 10)
		}
		drafts = append(drafts, d)
	}
	return &featuresv1.GetDraftsResponse{Drafts: drafts}, nil
}

// ClearDraft deletes the caller's draft for a room/channel. Requires
// authentication; a no-op if none exists.
func (s *Service) ClearDraft(ctx context.Context, req *featuresv1.ClearDraftRequest) (*emptypb.Empty, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	userUUID, _ := uuid.Parse(userID)
	roomID, channelID := parseOptionalUUIDs(req.RoomId, req.ChannelId)
	if err := s.repo.DeleteDraft(ctx, userUUID, roomID, channelID); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("clear failed", err))
	}
	return &emptypb.Empty{}, nil
}

// SetNotificationOverride saves the caller's notification override for a
// room/channel. A MuteDurationSeconds>0 sets mute_until to that far in the future.
// Requires authentication.
func (s *Service) SetNotificationOverride(ctx context.Context, req *featuresv1.SetNotificationOverrideRequest) (*emptypb.Empty, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	userUUID, _ := uuid.Parse(userID)
	roomID, channelID := parseOptionalUUIDs(req.RoomId, req.ChannelId)

	var muteUntil *time.Time
	if req.MuteDurationSeconds > 0 {
		t := time.Now().Add(time.Duration(req.MuteDurationSeconds) * time.Second)
		muteUntil = &t
	}

	if err := s.repo.UpsertNotificationOverride(ctx, userUUID, roomID, channelID, req.OverrideLevel, muteUntil, req.SuppressEveryone); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("save override failed", err))
	}
	return &emptypb.Empty{}, nil
}

// GetNotificationOverrides returns all of the caller's notification overrides.
// Requires authentication.
func (s *Service) GetNotificationOverrides(ctx context.Context, req *featuresv1.GetNotificationOverridesRequest) (*featuresv1.GetNotificationOverridesResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	userUUID, _ := uuid.Parse(userID)

	rows, err := s.repo.ListNotificationOverrides(ctx, userUUID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("query failed", err))
	}

	var overrides []*commonv1.NotificationOverride
	for _, row := range rows {
		o := &commonv1.NotificationOverride{OverrideLevel: row.OverrideLevel, SuppressEveryone: row.SuppressEveryone}
		if row.RoomID != nil {
			o.RoomId = row.RoomID.String()
		}
		if row.ChannelID != nil {
			o.ChannelId = row.ChannelID.String()
		}
		if row.MuteUntil != nil {
			o.MuteUntil = timestamppb.New(*row.MuteUntil)
		}
		overrides = append(overrides, o)
	}
	return &featuresv1.GetNotificationOverridesResponse{Overrides: overrides}, nil
}

// GetChannelMedia lists media attachments for a room or channel, optionally filtered
// by MediaType, with limit clamped to 1..100 (default 50) and one-extra over-fetch
// for HasMore pagination. The proto media_type is re-derived from each content type.
// Requires authentication.
func (s *Service) GetChannelMedia(ctx context.Context, req *featuresv1.GetChannelMediaRequest) (*featuresv1.GetChannelMediaResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}

	limit := int(req.Limit)
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	roomID, channelID := parseOptionalUUIDs(req.RoomId, req.ChannelId)
	rows, err := s.repo.ListChannelMedia(ctx, roomID, channelID, int(req.MediaType), limit+1)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("query failed", err))
	}

	var items []*commonv1.MediaItem
	for _, row := range rows {
		mt := int32(3)
		if len(row.ContentType) >= 6 && row.ContentType[:6] == "image/" {
			mt = 1
		} else if len(row.ContentType) >= 6 && row.ContentType[:6] == "video/" {
			mt = 2
		}
		item := &commonv1.MediaItem{
			MessageId: strconv.FormatInt(row.MessageID, 10), MediaType: mt,
			FileUrl: row.URL, MimeType: row.ContentType, CreatedAt: timestamppb.New(row.CreatedAt),
		}
		if row.Width != nil {
			item.Width = int32(*row.Width)
		}
		if row.Height != nil {
			item.Height = int32(*row.Height)
		}
		items = append(items, item)
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return &featuresv1.GetChannelMediaResponse{Items: items, HasMore: hasMore}, nil
}

// SetSlowMode sets a room's slow-mode interval. Requires authentication. Note: the
// Aggregator overrides this to delegate to the slow-mode service.
func (s *Service) SetSlowMode(ctx context.Context, req *featuresv1.SetSlowModeRequest) (*emptypb.Empty, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	roomUUID, _ := uuid.Parse(req.RoomId)
	if err := s.repo.SetSlowMode(ctx, roomUUID, req.IntervalSeconds); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("update failed", err))
	}
	return &emptypb.Empty{}, nil
}

// SuppressLinkPreview hides a specific link preview on a message (by url hash).
// Requires authentication.
func (s *Service) SuppressLinkPreview(ctx context.Context, req *featuresv1.SuppressLinkPreviewRequest) (*emptypb.Empty, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	msgID, _ := strconv.ParseInt(req.MessageId, 10, 64)
	if err := s.repo.SuppressLinkPreview(ctx, msgID, req.UrlHash); err != nil {
		return nil, errors.ToGRPCError(errors.Internal("suppress failed", err))
	}
	return &emptypb.Empty{}, nil
}

// GetStickerPacks returns the caller's sticker packs, each with its stickers
// loaded; a pack whose stickers fail to load is skipped. Requires authentication.
func (s *Service) GetStickerPacks(ctx context.Context, req *featuresv1.GetStickerPacksRequest) (*featuresv1.GetStickerPacksResponse, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.ToGRPCError(errors.Unauthorized("not authenticated"))
	}
	userUUID, _ := uuid.Parse(userID)

	packRows, err := s.repo.ListUserStickerPacks(ctx, userUUID)
	if err != nil {
		return nil, errors.ToGRPCError(errors.Internal("query failed", err))
	}

	var packs []*commonv1.StickerPack
	for _, p := range packRows {
		stickerRows, err := s.repo.ListStickersForPack(ctx, p.ID)
		if err != nil {
			continue
		}
		var stickers []*commonv1.Sticker
		for _, sr := range stickerRows {
			stickers = append(stickers, &commonv1.Sticker{
				Id: sr.ID.String(), PackId: sr.PackID.String(), Name: sr.Name, Tags: sr.Tags,
				FormatType: int32(sr.FormatType), FileUrl: sr.FileURL, Width: int32(sr.Width), Height: int32(sr.Height),
			})
		}
		packs = append(packs, &commonv1.StickerPack{
			Id: p.ID.String(), Name: p.Name, Description: p.Description,
			CreatorId: p.CreatorID.String(), Stickers: stickers,
		})
	}
	return &featuresv1.GetStickerPacksResponse{Packs: packs}, nil
}

// RunScheduler is the long-running delivery loop; it blocks until ctx is cancelled.
// Every 5s it delivers due scheduled messages and closes expired polls; every
// minute it recovers messages stuck in 'processing' (see scheduledStuckTimeout).
// Run it once per process (in a goroutine).
func (s *Service) RunScheduler(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	recoverTicker := time.NewTicker(time.Minute)
	defer recoverTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-recoverTicker.C:
			if n, err := s.repo.RecoverStuckScheduledMessages(ctx, scheduledStuckTimeout); err != nil {
				s.logger.Warn("recover stuck scheduled messages failed", zap.Error(err))
			} else if n > 0 {
				s.logger.Info("recovered stuck scheduled messages", zap.Int64("count", n))
			}
		case <-ticker.C:
			s.processScheduledMessages(ctx)
			if err := s.repo.CloseExpiredPolls(ctx); err != nil {
				s.logger.Warn("close expired polls failed", zap.Error(err))
			}
		}
	}
}

// processScheduledMessages claims and delivers up to 10 due messages per tick,
// draining in batches so one slow tick doesn't monopolize the loop. It stops early
// when nothing is due or a claim errors.
func (s *Service) processScheduledMessages(ctx context.Context) {
	for i := 0; i < 10; i++ {
		row, err := s.repo.ClaimNextScheduledMessage(ctx)
		if err != nil {
			s.logger.Warn("claim scheduled message failed", zap.Error(err))
			return
		}
		if row == nil {
			return
		}
		s.deliverScheduledMessage(ctx, row)
	}
}

// deliverScheduledMessage delivers one claimed message: it allocates (idempotently,
// via GetOrSetScheduledMessageID) the final message ID, inserts the message into
// the room or DM, marks the row sent, and broadcasts room messages. Failures are
// recorded via MarkScheduledFailed with retry semantics (a missing destination is a
// permanent, non-retryable failure).
func (s *Service) deliverScheduledMessage(ctx context.Context, row *ScheduledMessageRow) {
	provisionalID := s.snowflake.Generate()
	msgID, err := s.repo.GetOrSetScheduledMessageID(ctx, row.ID, provisionalID)
	if err != nil {
		_ = s.repo.MarkScheduledFailed(ctx, row.ID, "id allocation failed: "+err.Error(), true, scheduledMaxAttempts)
		return
	}
	createdAt := s.snowflake.ExtractTimestamp(msgID)

	var sendErr error
	if row.RoomID != nil {
		sendErr = s.repo.InsertRoomMessage(ctx, msgID, *row.RoomID, row.AuthorID, row.Content, createdAt, row.ReplyToID)
	} else if row.ChannelID != nil {
		sendErr = s.repo.InsertDMMessage(ctx, msgID, *row.ChannelID, row.AuthorID, row.Content, createdAt, row.ReplyToID)
	} else {
		_ = s.repo.MarkScheduledFailed(ctx, row.ID, "no destination", false, scheduledMaxAttempts)
		return
	}

	if sendErr != nil {
		if err := s.repo.MarkScheduledFailed(ctx, row.ID, sendErr.Error(), true, scheduledMaxAttempts); err != nil {
			s.logger.Error("mark scheduled failed errored", zap.Int64("id", row.ID), zap.Error(err))
		}
		return
	}

	if err := s.repo.MarkScheduledSent(ctx, row.ID); err != nil {
		s.logger.Error("mark scheduled sent errored", zap.Int64("id", row.ID), zap.Error(err))
		return
	}

	if row.RoomID != nil && s.hub != nil {
		s.hub.BroadcastToRoom(row.RoomID.String(), &streamv1.ServerEvent{
			EventId: uuid.New().String(), CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_MessageCreated{MessageCreated: &streamv1.MessageCreated{
				Message: &commonv1.Message{
					Id: strconv.FormatInt(msgID, 10), RoomId: row.RoomID.String(),
					AuthorId: row.AuthorID.String(), Content: row.Content,
					CreatedAt: timestamppb.New(createdAt),
				},
			}},
		})
	}
}

// loadProtoPoll loads a poll and converts it to its proto form, including the
// user's own votes; it returns nil if the poll can't be loaded.
func (s *Service) loadProtoPoll(ctx context.Context, pollID, userID uuid.UUID) *commonv1.Poll {
	pollRow, options, myVotes, err := s.repo.LoadPoll(ctx, pollID, userID)
	if err != nil {
		return nil
	}

	protoOpts := make([]*commonv1.PollOption, len(options))
	for i, o := range options {
		protoOpts[i] = &commonv1.PollOption{OptionId: int32(o.OptionID), Text: o.Text, VoteCount: int32(o.VoteCount)}
	}

	myVoteStrs := make([]string, len(myVotes))
	for i, v := range myVotes {
		myVoteStrs[i] = strconv.Itoa(v)
	}

	poll := &commonv1.Poll{
		Id: pollID.String(), Question: pollRow.Question, Options: protoOpts,
		PollType: int32(pollRow.PollType), IsAnonymous: pollRow.IsAnonymous, AllowsMultiple: pollRow.AllowsMultiple,
		IsClosed: pollRow.IsClosed, TotalVoters: int32(pollRow.TotalVoters), MyVotes: myVoteStrs,
	}
	if pollRow.CorrectOption != nil {
		poll.CorrectOption = int32(*pollRow.CorrectOption)
	}
	if pollRow.Explanation != nil {
		poll.Explanation = *pollRow.Explanation
	}
	if pollRow.CloseDate != nil {
		poll.CloseDate = timestamppb.New(*pollRow.CloseDate)
	}
	return poll
}

// parseOptionalUUIDs parses room/channel ID strings into pointers, returning nil
// for an empty string; parse errors are ignored (yielding a pointer to the zero
// UUID), matching the callers' best-effort handling.
func parseOptionalUUIDs(roomIDStr, channelIDStr string) (*uuid.UUID, *uuid.UUID) {
	var roomID, channelID *uuid.UUID
	if roomIDStr != "" {
		r, _ := uuid.Parse(roomIDStr)
		roomID = &r
	}
	if channelIDStr != "" {
		c, _ := uuid.Parse(channelIDStr)
		channelID = &c
	}
	return roomID, channelID
}
