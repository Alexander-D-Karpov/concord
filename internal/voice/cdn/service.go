package cdn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"github.com/google/uuid"
	"strings"
	"time"
)

type Service struct {
	repo          *Repository
	healthChecker *HealthChecker
	officialCDNs  []string
}

func NewService(repo *Repository, officialCDNs []string) *Service {
	return &Service{
		repo:          repo,
		healthChecker: NewHealthChecker(5 * time.Second),
		officialCDNs:  officialCDNs,
	}
}

func (s *Service) RegisterUserCDN(ctx context.Context, name, region, addrUDP, addrCtrl string) (*CDNServer, string, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, "", errors.Unauthorized("user not authenticated")
	}
	ownerUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, "", errors.BadRequest("invalid user id")
	}
	token := s.generateToken()
	cdn := &CDNServer{
		Name:         name,
		Region:       region,
		AddrUDP:      addrUDP,
		AddrCtrl:     addrCtrl,
		OwnerUserID:  &ownerUUID,
		AccessToken:  &token,
		Status:       "online",
		CapacityHint: 100,
		LoadScore:    0.0,
		IsOfficial:   false,
	}
	if err := s.repo.Create(ctx, cdn); err != nil {
		return nil, "", err
	}
	return cdn, token, nil
}

func (s *Service) ListAvailableCDNs(ctx context.Context, region *string) ([]*CDNServer, error) {
	official, err := s.repo.ListOfficial(ctx, region)
	if err != nil {
		return nil, err
	}
	if len(s.officialCDNs) > 0 {
		healthResults := s.healthChecker.CheckMultiple(ctx, s.officialCDNs)
		for _, cdn := range official {
			if result, exists := healthResults[cdn.AddrUDP]; exists {
				if result.Healthy {
					cdn.Latency = &result.Latency
					_ = s.repo.UpdateStatus(ctx, cdn.ID, "online", cdn.LoadScore, cdn.Latency)
				} else {
					_ = s.repo.UpdateStatus(ctx, cdn.ID, "offline", cdn.LoadScore, nil)
				}
			}
		}
	}
	return official, nil
}

func (s *Service) GetUserCDNs(ctx context.Context) ([]*CDNServer, error) {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return nil, errors.Unauthorized("user not authenticated")
	}
	ownerUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.BadRequest("invalid user id")
	}
	return s.repo.ListByOwner(ctx, ownerUUID)
}

func (s *Service) DeleteUserCDN(ctx context.Context, cdnID string) error {
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		return errors.Unauthorized("user not authenticated")
	}
	ownerUUID, err := uuid.Parse(userID)
	if err != nil {
		return errors.BadRequest("invalid user id")
	}
	id, err := uuid.Parse(cdnID)
	if err != nil {
		return errors.BadRequest("invalid CDN id")
	}
	return s.repo.Delete(ctx, id, &ownerUUID)
}

func (s *Service) FormatCDNURL(cdn *CDNServer, includeToken bool) string {
	url := fmt.Sprintf("udp://%s", cdn.AddrUDP)
	if includeToken && cdn.AccessToken != nil {
		url = fmt.Sprintf("%s?token=%s", url, *cdn.AccessToken)
	}
	return url
}

func (s *Service) generateToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

func (s *Service) ParseCDNURL(cdnURL string) (string, string, error) {
	parts := strings.Split(cdnURL, "?token=")
	if len(parts) != 2 {
		return "", "", errors.BadRequest("invalid CDN URL format")
	}
	addr := strings.TrimPrefix(parts[0], "udp://")
	token := parts[1]
	return addr, token, nil
}

func (s *Service) GetOfficialCDNList() []string {
	return s.officialCDNs
}
