package integration_test

import (
	"context"
	"testing"

	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/Alexander-D-Karpov/concord/tests/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewUserStatusPreferenceDefaultsOnline guards the registration regression where
// a freshly created user's stored status preference defaulted to "offline", which
// EffectiveStatus treats as an absolute override — so the account appeared offline
// forever even after connecting. A new user must default to the "online" preference.
func TestNewUserStatusPreferenceDefaultsOnline(t *testing.T) {
	database := testutil.GetDB(t)
	repo := users.NewRepository(database.Pool)
	ctx := context.Background()

	user := &users.User{
		Handle:      uniqueHandle("statusdefault"),
		DisplayName: "Status Default User",
	}
	require.NoError(t, repo.Create(ctx, user))

	pref, err := repo.GetStatusPreference(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, users.StatusOnline, pref, "newly registered user should default to online preference")
}
