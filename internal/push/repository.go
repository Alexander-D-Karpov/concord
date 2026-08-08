// Package push implements device registration and FCM push dispatch so backgrounded
// mobile clients receive message notifications and incoming-call rings.
package push

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Device is a registered push target for a user.
type Device struct {
	UserID     uuid.UUID
	DeviceID   string
	Platform   string
	FCMToken   string
	AppVersion string
	Locale     string
	UpdatedAt  time.Time
}

// Repository is the push_devices data-access layer.
type Repository struct{ pool *pgxpool.Pool }

// NewRepository builds a push device repository.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Upsert inserts or updates the (user, device) row so a token rotation replaces the
// prior token rather than creating a second device.
func (r *Repository) Upsert(ctx context.Context, d Device) error {
	if d.Platform == "" {
		d.Platform = "android"
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO push_devices (user_id, device_id, platform, fcm_token, app_version, locale, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (user_id, device_id) DO UPDATE SET
			platform = EXCLUDED.platform,
			fcm_token = EXCLUDED.fcm_token,
			app_version = EXCLUDED.app_version,
			locale = EXCLUDED.locale,
			updated_at = NOW()
	`, d.UserID, d.DeviceID, d.Platform, d.FCMToken, d.AppVersion, d.Locale)
	return err
}

// ListByUser returns all devices registered for userID.
func (r *Repository) ListByUser(ctx context.Context, userID uuid.UUID) ([]Device, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, device_id, platform, fcm_token, app_version, locale, updated_at
		FROM push_devices WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.UserID, &d.DeviceID, &d.Platform, &d.FCMToken, &d.AppVersion, &d.Locale, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteByUserDevice removes one device (explicit unregister), reporting whether a
// row was removed.
func (r *Repository) DeleteByUserDevice(ctx context.Context, userID uuid.UUID, deviceID string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM push_devices WHERE user_id = $1 AND device_id = $2`, userID, deviceID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteByToken removes any device holding fcmToken. Used for self-healing when FCM
// reports the token unregistered.
func (r *Repository) DeleteByToken(ctx context.Context, fcmToken string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM push_devices WHERE fcm_token = $1`, fcmToken)
	return err
}
