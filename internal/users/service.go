package users

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
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
		user.Bio = &bio
	}

	if err := s.repo.Update(ctx, user); err != nil {
		return nil, err
	}

	return user, nil
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

	return s.repo.GetByID(ctx, id)
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

	userID := interceptor.GetUserID(ctx)
	var excludeUserID *uuid.UUID
	if userID != "" {
		id, err := uuid.Parse(userID)
		if err == nil {
			excludeUserID = &id
		}
	}

	users, err := s.repo.Search(ctx, query, excludeUserID, limit+1, offset)
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
