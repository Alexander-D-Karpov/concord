package auth

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/auth/jwt"
)

// Validator authenticates voice clients by verifying short-lived voice JWTs
// against the shared jwt.Manager. It is stateless and safe for concurrent use.
type Validator struct {
	jwtManager *jwt.Manager
}

// NewValidator returns a Validator backed by jwtManager.
func NewValidator(jwtManager *jwt.Manager) *Validator {
	return &Validator{
		jwtManager: jwtManager,
	}
}

// ValidateToken verifies a voice token and returns its claims, or an error
// wrapping the cause when the token is invalid or expired. ctx is accepted for
// interface symmetry but not currently used by the underlying verification.
func (v *Validator) ValidateToken(ctx context.Context, token string) (*jwt.Claims, error) {
	claims, err := v.jwtManager.ValidateVoiceToken(token)
	if err != nil {
		return nil, fmt.Errorf("invalid voice token: %w", err)
	}
	return claims, nil
}
