package users

import (
	"context"
	"fmt"

	streamv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/stream/v1"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/Alexander-D-Karpov/concord/internal/events"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	repo        *Repository
	hub         *events.Hub
	storagePath string
	storageURL  string
}

func NewService(repo *Repository, hub *events.Hub, storagePath, storageURL string) *Service {
	return &Service{
		repo:        repo,
		hub:         hub,
		storagePath: storagePath,
		storageURL:  storageURL,
	}
}

func (s *Service) GetSelf(ctx context.Context) (*User, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}
	return s.repo.GetByID(ctx, id)
}

func (s *Service) GetUser(ctx context.Context, userID string) (*User, error) {
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}
	return s.repo.GetByID(ctx, id)
}

func (s *Service) GetUserByHandle(ctx context.Context, handle string) (*User, error) {
	return s.repo.GetByHandle(ctx, handle)
}

func (s *Service) UpdateProfile(ctx context.Context, displayName, avatarURL, bio string) (*User, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if displayName != "" {
		user.DisplayName = displayName
	}
	if avatarURL != "" {
		user.AvatarURL = avatarURL
	}
	if bio != "" {
		user.Bio = bio
	}

	if err := s.repo.Update(ctx, user); err != nil {
		return nil, err
	}

	if s.hub != nil {
		friends, err := s.getFriendsList(ctx, id)
		if err == nil {
			profileEvent := &streamv1.ServerEvent{
				EventId:   uuid.New().String(),
				CreatedAt: timestamppb.Now(),
				Payload: &streamv1.ServerEvent_ProfileUpdated{
					ProfileUpdated: &streamv1.ProfileUpdated{
						UserId:      userID,
						DisplayName: user.DisplayName,
						AvatarUrl:   user.AvatarURL,
						Status:      user.Status,
						Bio:         user.Bio,
					},
				},
			}
			for _, friendID := range friends {
				s.hub.BroadcastToUser(friendID.String(), profileEvent)
			}
		}
	}

	return user, nil
}

func (s *Service) UploadAvatar(ctx context.Context, imageData []byte, filename string) (*User, *UserAvatar, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, nil, errors.Unauthorized("user not authenticated")
	}
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, nil, errors.BadRequest("invalid user id")
	}

	processed, err := ProcessAvatarImage(imageData)
	if err != nil {
		return nil, nil, errors.BadRequest(fmt.Sprintf("invalid image: %v", err))
	}

	fullRel, thumbRel, err := SaveAvatarFiles(s.storagePath, userID, processed.FullData, processed.ThumbData)
	if err != nil {
		return nil, nil, errors.Internal("failed to save avatar files", err)
	}

	fullURL := s.storageURL + "/" + fullRel
	thumbURL := s.storageURL + "/" + thumbRel

	count, err := s.repo.CountUserAvatars(ctx, id)
	if err != nil {
		return nil, nil, errors.Internal("failed to count avatars", err)
	}
	if count >= MaxAvatarHistory {
		oldest, err := s.repo.GetOldestUserAvatar(ctx, id)
		if err == nil && oldest != nil {
			DeleteAvatarFiles(s.storagePath, oldest.FullURL, oldest.ThumbnailURL)
			_ = s.repo.DeleteUserAvatar(ctx, oldest.ID)
		}
	}

	av := &UserAvatar{
		UserID:           id,
		FullURL:          fullURL,
		ThumbnailURL:     thumbURL,
		OriginalFilename: filename,
		SizeBytes:        int64(len(imageData)),
	}
	if err := s.repo.CreateUserAvatar(ctx, av); err != nil {
		return nil, nil, errors.Internal("failed to save avatar record", err)
	}

	if err := s.repo.UpdateAvatar(ctx, id, fullURL, thumbURL); err != nil {
		return nil, nil, errors.Internal("failed to update user avatar", err)
	}

	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	if s.hub != nil {
		friends, _ := s.getFriendsList(ctx, id)
		profileEvent := &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_ProfileUpdated{
				ProfileUpdated: &streamv1.ProfileUpdated{
					UserId:      userID,
					DisplayName: user.DisplayName,
					AvatarUrl:   user.AvatarURL,
					Status:      user.Status,
					Bio:         user.Bio,
				},
			},
		}
		for _, fid := range friends {
			s.hub.BroadcastToUser(fid.String(), profileEvent)
		}
	}

	return user, av, nil
}

func (s *Service) DeleteAvatar(ctx context.Context, avatarID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}
	avID, err := uuid.Parse(avatarID)
	if err != nil {
		return errors.BadRequest("invalid avatar id")
	}

	av, err := s.repo.GetUserAvatar(ctx, avID)
	if err != nil {
		return err
	}
	if av.UserID != uid {
		return errors.Forbidden("not your avatar")
	}

	DeleteAvatarFiles(s.storagePath, av.FullURL, av.ThumbnailURL)

	if err := s.repo.DeleteUserAvatar(ctx, avID); err != nil {
		return err
	}

	user, _ := s.repo.GetByID(ctx, uid)
	if user != nil && (user.AvatarURL == av.FullURL) {
		latest, _ := s.repo.GetLatestUserAvatar(ctx, uid)
		if latest != nil {
			_ = s.repo.UpdateAvatar(ctx, uid, latest.FullURL, latest.ThumbnailURL)
		} else {
			_ = s.repo.UpdateAvatar(ctx, uid, "", "")
		}
	}

	return nil
}

func (s *Service) GetAvatarHistory(ctx context.Context, userID string) ([]*UserAvatar, error) {
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}
	return s.repo.ListUserAvatars(ctx, id, MaxAvatarHistory)
}

func (s *Service) UpdateStatus(ctx context.Context, status string) (*User, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}
	if err := s.repo.UpdateStatus(ctx, id, status); err != nil {
		return nil, err
	}
	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if s.hub != nil {
		friends, err := s.getFriendsList(ctx, id)
		if err == nil {
			statusEvent := &streamv1.ServerEvent{
				EventId:   uuid.New().String(),
				CreatedAt: timestamppb.Now(),
				Payload: &streamv1.ServerEvent_UserStatusChanged{
					UserStatusChanged: &streamv1.UserStatusChanged{
						UserId: userID,
						Status: status,
					},
				},
			}
			for _, friendID := range friends {
				s.hub.BroadcastToUser(friendID.String(), statusEvent)
			}
		}
	}
	return user, nil
}

func (s *Service) getFriendsList(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	query := `
		SELECT CASE WHEN user_id1 = $1 THEN user_id2 ELSE user_id1 END as friend_id
		FROM friendships WHERE user_id1 = $1 OR user_id2 = $1
	`
	rows, err := s.repo.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var friends []uuid.UUID
	for rows.Next() {
		var friendID uuid.UUID
		if err := rows.Scan(&friendID); err != nil {
			return nil, err
		}
		friends = append(friends, friendID)
	}
	return friends, rows.Err()
}

func (s *Service) SearchUsers(ctx context.Context, query string, limit int, cursor *string) ([]*User, *string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var offset int
	if cursor != nil && *cursor != "" {
		if _, err := fmt.Sscanf(*cursor, "%d", &offset); err != nil {
			return nil, nil, errors.BadRequest("invalid cursor")
		}
	}
	users, err := s.repo.Search(ctx, query, limit)
	if err != nil {
		return nil, nil, err
	}
	var nextCursor *string
	if len(users) > limit {
		users = users[:limit]
		next := fmt.Sprintf("%d", offset+limit)
		nextCursor = &next
	}
	return users, nextCursor, nil
}

func (s *Service) ListUsersByIDs(ctx context.Context, userIDs []string) ([]*User, error) {
	ids := make([]uuid.UUID, 0, len(userIDs))
	for _, idStr := range userIDs {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return s.repo.ListByIDs(ctx, ids)
}
