package auth

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
)

type Validator struct {
	jwtManager *jwt.Manager
}

func NewValidator(jwtManager *jwt.Manager) *Validator {
	return &Validator{
		jwtManager: jwtManager,
	}
}

func (v *Validator) ValidateToken(ctx context.Context, token string) (*jwt.Claims, error) {
	claims, err := v.jwtManager.ValidateVoiceToken(token)
	if err != nil {
		return nil, fmt.Errorf("invalid voice token: %w", err)
	}
	return claims, nil
}
