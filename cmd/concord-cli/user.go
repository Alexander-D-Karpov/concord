package main

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"
)

// userCmd groups the user-administration subcommands.
func userCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Manage users"}
	cmd.AddCommand(userCreateCmd(), userSetPasswordCmd(), userUnlockCmd(), userSetRoleCmd())
	return cmd
}

// resolveUserID resolves a handle or UUID string to a user UUID.
func resolveUserID(ctx context.Context, pool *pgxpool.Pool, s string) (uuid.UUID, error) {
	if id, err := uuid.Parse(s); err == nil {
		return id, nil
	}
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM users WHERE handle = $1`, s).Scan(&id)
	if err == pgx.ErrNoRows {
		return uuid.Nil, fmt.Errorf("no user with handle %q", s)
	}
	return id, err
}

func userCreateCmd() *cobra.Command {
	var handle, password, displayName string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a user with a password",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if handle == "" || password == "" {
				return fmt.Errorf("--handle and --password are required")
			}
			if len(password) < 6 {
				return fmt.Errorf("password must be at least 6 characters")
			}
			if displayName == "" {
				displayName = handle
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, _ *cache.Cache) error {
				var id uuid.UUID
				err := pool.QueryRow(ctx,
					`INSERT INTO users (handle, display_name, password_hash) VALUES ($1, $2, $3) RETURNING id`,
					handle, displayName, string(hash),
				).Scan(&id)
				if err != nil {
					return fmt.Errorf("create user (handle may be taken): %w", err)
				}
				fmt.Printf("created user %s (%s)\n", handle, id)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&handle, "handle", "", "user handle (required)")
	cmd.Flags().StringVar(&password, "password", "", "password (required, min 6 chars)")
	cmd.Flags().StringVar(&displayName, "display-name", "", "display name (defaults to handle)")
	return cmd
}

func userSetPasswordCmd() *cobra.Command {
	var handle, password string
	cmd := &cobra.Command{
		Use:   "set-password",
		Short: "Set a user's password",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if handle == "" || password == "" {
				return fmt.Errorf("--handle and --password are required")
			}
			if len(password) < 6 {
				return fmt.Errorf("password must be at least 6 characters")
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, c *cache.Cache) error {
				ok, err := usersRepo(pool, c).UpdatePasswordByHandle(ctx, handle, string(hash))
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("no user with handle %q", handle)
				}
				fmt.Printf("password updated for %s\n", handle)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&handle, "handle", "", "user handle (required)")
	cmd.Flags().StringVar(&password, "password", "", "new password (required, min 6 chars)")
	return cmd
}

func userUnlockCmd() *cobra.Command {
	var handle string
	cmd := &cobra.Command{
		Use:   "unlock",
		Short: "Clear a user's login lockout (failed-attempt counter + lock)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if handle == "" {
				return fmt.Errorf("--handle is required")
			}
			return withCache(func(ctx context.Context, _ *config.Config, c *cache.Cache) error {
				if err := c.Delete(ctx,
					fmt.Sprintf("login_attempts:%s", handle),
					fmt.Sprintf("account_locked:%s", handle),
				); err != nil {
					return err
				}
				fmt.Printf("cleared login lockout for %s\n", handle)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&handle, "handle", "", "user handle (required)")
	return cmd
}

func userSetRoleCmd() *cobra.Command {
	var room, user, role string
	cmd := &cobra.Command{
		Use:   "set-role",
		Short: "Set a user's role in a room (member|moderator|admin)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if room == "" || user == "" || role == "" {
				return fmt.Errorf("--room, --user, and --role are required")
			}
			if role != "member" && role != "moderator" && role != "admin" {
				return fmt.Errorf("--role must be member, moderator, or admin")
			}
			roomID, err := uuid.Parse(room)
			if err != nil {
				return fmt.Errorf("invalid --room (want a UUID)")
			}
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, c *cache.Cache) error {
				userID, err := resolveUserID(ctx, pool, user)
				if err != nil {
					return err
				}
				ok, err := roomsRepo(pool, c).SetMemberRole(ctx, roomID, userID, role)
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("user is not a member of that room")
				}
				fmt.Printf("set role of %s in room %s to %s\n", user, room, role)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&room, "room", "", "room UUID (required)")
	cmd.Flags().StringVar(&user, "user", "", "user handle or UUID (required)")
	cmd.Flags().StringVar(&role, "role", "", "member|moderator|admin (required)")
	return cmd
}
