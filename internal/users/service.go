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

// Service holds user business logic: profile/status updates, avatar processing and
// storage, and friend-scoped event broadcasting. storagePath is the local dir for
// avatar files and storageURL is the public base used to build their URLs.
type Service struct {
	repo        *Repository
	hub         *events.Hub
	presence    *PresenceManager
	storagePath string
	storageURL  string
}

// NewService constructs a Service from its repository, event hub, presence
// manager, and avatar storage path/URL.
func NewService(repo *Repository, hub *events.Hub, presence *PresenceManager, storagePath, storageURL string) *Service {
	return &Service{
		repo:        repo,
		hub:         hub,
		presence:    presence,
		storagePath: storagePath,
		storageURL:  storageURL,
	}
}

// NormalizeStatusPreference collapses a raw status string to a valid preference:
// StatusDND, StatusOffline (also for "invisible"), or StatusOnline as the default.
func NormalizeStatusPreference(status string) string {
	switch status {
	case StatusDND:
		return StatusDND
	case StatusOffline, "invisible":
		return StatusOffline
	default:
		return StatusOnline
	}
}

// EffectiveStatus combines a user's chosen preference with their live presence to
// produce the status others should see: an offline preference always shows
// offline, DND shows dnd unless actually offline, and otherwise the raw presence
// (online/away) is surfaced, defaulting to offline.
func EffectiveStatus(statusPreference string, presence string) string {
	statusPreference = NormalizeStatusPreference(statusPreference)

	if statusPreference == StatusOffline {
		return StatusOffline
	}

	if statusPreference == StatusDND {
		if presence == StatusOffline {
			return StatusOffline
		}
		return StatusDND
	}

	switch presence {
	case StatusAway:
		return StatusAway
	case StatusOnline:
		return StatusOnline
	default:
		return StatusOffline
	}
}

// currentPresence returns the user's live presence from the manager, or
// StatusOffline when no presence manager is configured.
func (s *Service) currentPresence(userID uuid.UUID) string {
	if s.presence == nil {
		return StatusOffline
	}
	return s.presence.GetStatus(userID)
}

// decorateSelfUserStatus fills in a user shown to themselves: it sets
// StatusPreference to the stored preference and Status to the effective status.
func (s *Service) decorateSelfUserStatus(user *User) {
	if user == nil {
		return
	}

	storedPreference := NormalizeStatusPreference(user.Status)
	user.StatusPreference = storedPreference
	user.Status = EffectiveStatus(storedPreference, s.currentPresence(user.ID))
}

// decoratePublicUserStatus fills in a user shown to others: it sets Status to the
// effective status and clears StatusPreference so the raw preference is not leaked.
func (s *Service) decoratePublicUserStatus(user *User) {
	if user == nil {
		return
	}

	storedPreference := NormalizeStatusPreference(user.Status)
	user.Status = EffectiveStatus(storedPreference, s.currentPresence(user.ID))
	user.StatusPreference = ""
}

// decoratePublicUsersStatus applies decoratePublicUserStatus to each user in the slice.
func (s *Service) decoratePublicUsersStatus(users []*User) {
	for _, user := range users {
		s.decoratePublicUserStatus(user)
	}
}

// GetSelf returns the authenticated caller's user (self-decorated). Returns
// Unauthorized if no user is in the context or BadRequest for a malformed ID.
func (s *Service) GetSelf(ctx context.Context) (*User, error) {
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

	s.decorateSelfUserStatus(user)
	return user, nil
}

// GetUser returns a public-decorated view of the user with the given ID; returns
// BadRequest for a malformed ID.
func (s *Service) GetUser(ctx context.Context, userID string) (*User, error) {
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	s.decoratePublicUserStatus(user)
	return user, nil
}

// GetUserByHandle returns a public-decorated view of the user with the given handle.
func (s *Service) GetUserByHandle(ctx context.Context, handle string) (*User, error) {
	user, err := s.repo.GetByHandle(ctx, handle)
	if err != nil {
		return nil, err
	}

	s.decoratePublicUserStatus(user)
	return user, nil
}

// UpdateProfile updates the caller's non-empty display name, avatar URL, and bio,
// then re-reads and returns the self-decorated user. On success it broadcasts a
// ProfileUpdated event (with a public-decorated payload) to the caller's friends.
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

	user, err = s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	s.decorateSelfUserStatus(user)

	if s.hub != nil {
		friends, err := s.getFriendsList(ctx, id)
		if err == nil {
			publicUser := *user
			s.decoratePublicUserStatus(&publicUser)

			profileEvent := &streamv1.ServerEvent{
				EventId:   uuid.New().String(),
				CreatedAt: timestamppb.Now(),
				Payload: &streamv1.ServerEvent_ProfileUpdated{
					ProfileUpdated: &streamv1.ProfileUpdated{
						UserId:      userID,
						DisplayName: publicUser.DisplayName,
						AvatarUrl:   publicUser.AvatarURL,
						Status:      publicUser.Status,
						Bio:         publicUser.Bio,
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

// UploadAvatar processes the image, writes the avatar files, records a history
// entry, and sets it as the user's current avatar. When history exceeds
// MaxAvatarHistory the oldest entry and its files are pruned first. On success it
// broadcasts a ProfileUpdated event to the caller's friends and returns the
// self-decorated user plus the new history entry.
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

	s.decorateSelfUserStatus(user)

	if s.hub != nil {
		friends, _ := s.getFriendsList(ctx, id)

		publicUser := *user
		s.decoratePublicUserStatus(&publicUser)

		profileEvent := &streamv1.ServerEvent{
			EventId:   uuid.New().String(),
			CreatedAt: timestamppb.Now(),
			Payload: &streamv1.ServerEvent_ProfileUpdated{
				ProfileUpdated: &streamv1.ProfileUpdated{
					UserId:      userID,
					DisplayName: publicUser.DisplayName,
					AvatarUrl:   publicUser.AvatarURL,
					Status:      publicUser.Status,
					Bio:         publicUser.Bio,
				},
			},
		}

		for _, fid := range friends {
			s.hub.BroadcastToUser(fid.String(), profileEvent)
		}
	}

	return user, av, nil
}

// DeleteAvatar removes one of the caller's avatar history entries and its files
// after verifying ownership (Forbidden otherwise). If the deleted avatar was the
// user's current one, it falls back to the latest remaining avatar, or clears the
// avatar if none remain.
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

// GetAvatarHistory returns up to MaxAvatarHistory of the given user's avatars,
// newest-first; returns BadRequest for a malformed ID.
func (s *Service) GetAvatarHistory(ctx context.Context, userID string) ([]*UserAvatar, error) {
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}
	return s.repo.ListUserAvatars(ctx, id, MaxAvatarHistory)
}

// UpdateStatus normalizes and persists the caller's status preference, then
// re-broadcasts their effective presence to friends and shared rooms, returning
// the self-decorated user.
func (s *Service) UpdateStatus(ctx context.Context, status string) (*User, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}

	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}

	preference := NormalizeStatusPreference(status)

	if err := s.repo.UpdateStatus(ctx, id, preference); err != nil {
		return nil, err
	}

	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	s.decorateSelfUserStatus(user)

	if s.presence != nil {
		s.presence.broadcastCurrentStatus(ctx, id)
	}

	return user, nil
}

// getFriendsList returns the IDs of the user's friends, resolving each
// friendship's ordered pair to the other participant.
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

// SearchUsers returns public-decorated users matching query with offset-based
// pagination encoded in the opaque cursor (limit clamped to 1..100, default 50).
// It over-fetches by one to compute the next cursor, which is nil on the last page.
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

	users, err := s.repo.Search(ctx, query, limit+1)
	if err != nil {
		return nil, nil, err
	}

	var nextCursor *string
	if len(users) > limit {
		users = users[:limit]
		next := fmt.Sprintf("%d", offset+limit)
		nextCursor = &next
	}

	s.decoratePublicUsersStatus(users)
	return users, nextCursor, nil
}

// ListUsersByIDs returns public-decorated users for the given string IDs,
// silently skipping any that fail to parse as UUIDs.
func (s *Service) ListUsersByIDs(ctx context.Context, userIDs []string) ([]*User, error) {
	ids := make([]uuid.UUID, 0, len(userIDs))
	for _, idStr := range userIDs {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}

	users, err := s.repo.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	s.decoratePublicUsersStatus(users)
	return users, nil
}
