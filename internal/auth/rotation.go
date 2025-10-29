package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type TokenRotation struct {
	pool *pgxpool.Pool
}

func NewTokenRotation(pool *pgxpool.Pool) *TokenRotation {
	return &TokenRotation{pool: pool}
}

func (tr *TokenRotation) RotateRefreshToken(ctx context.Context, oldToken string, newToken string, userID string, expiresAt time.Time) error {
	oldHash := hashToken(oldToken)
	newHash := hashToken(newToken)

	tx, err := tr.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			return
		}
	}()

	_, err = tx.Exec(ctx, `
		UPDATE refresh_tokens 
		SET revoked_at = NOW() 
		WHERE token_hash = $1 AND user_id = $2
	`, oldHash, userID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO refresh_tokens (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)
	`, newHash, userID, expiresAt)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
