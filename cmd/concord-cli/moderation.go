package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Alexander-D-Karpov/concord/internal/audit"
	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// banActor resolves the user id to attribute a ban to: the --by flag (handle/UUID)
// if given, otherwise the room's creator (a guaranteed-valid user for the FK).
func banActor(ctx context.Context, pool *pgxpool.Pool, roomID uuid.UUID, by string) (uuid.UUID, error) {
	if by != "" {
		return resolveUserID(ctx, pool, by)
	}
	var creator uuid.UUID
	err := pool.QueryRow(ctx, `SELECT created_by FROM rooms WHERE id = $1`, roomID).Scan(&creator)
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve room creator for ban attribution: %w", err)
	}
	return creator, nil
}

func banCmd() *cobra.Command {
	var room, user, by string
	var duration int64
	cmd := &cobra.Command{
		Use:   "ban",
		Short: "Ban a user from a room",
		RunE: func(cmd *cobra.Command, _ []string) error {
			roomID, err := uuid.Parse(room)
			if err != nil {
				return fmt.Errorf("invalid --room (want a UUID)")
			}
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, c *cache.Cache) error {
				targetID, err := resolveUserID(ctx, pool, user)
				if err != nil {
					return err
				}
				actor, err := banActor(ctx, pool, roomID, by)
				if err != nil {
					return err
				}
				var expiresAt *time.Time
				if duration > 0 {
					t := time.Now().Add(time.Duration(duration) * time.Second)
					expiresAt = &t
				}
				repo := roomsRepo(pool, c)
				if err := repo.AddBan(ctx, roomID, targetID, actor, expiresAt); err != nil {
					return err
				}
				if err := repo.RemoveMember(ctx, roomID, targetID); err != nil {
					// Not a member (or already removed) is fine; the ban still applies.
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "note: could not remove membership: %v\n", err)
				}
				_ = audit.NewLogger(pool, zap.NewNop()).Log(ctx, audit.Event{
					RoomID: room, UserID: actor.String(), Action: "user.ban",
					ResourceID: targetID.String(), ResourceType: "user",
					Metadata: map[string]interface{}{"via": "cli", "duration_seconds": duration},
				})
				fmt.Printf("banned %s from room %s\n", user, room)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&room, "room", "", "room UUID (required)")
	cmd.Flags().StringVar(&user, "user", "", "user handle or UUID (required)")
	cmd.Flags().StringVar(&by, "by", "", "attribute the ban to this handle/UUID (default: room creator)")
	cmd.Flags().Int64Var(&duration, "duration", 0, "ban duration in seconds (0 = permanent)")
	return cmd
}

func unbanCmd() *cobra.Command {
	var room, user string
	cmd := &cobra.Command{
		Use:   "unban",
		Short: "Lift a user's ban from a room",
		RunE: func(cmd *cobra.Command, _ []string) error {
			roomID, err := uuid.Parse(room)
			if err != nil {
				return fmt.Errorf("invalid --room (want a UUID)")
			}
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, c *cache.Cache) error {
				targetID, err := resolveUserID(ctx, pool, user)
				if err != nil {
					return err
				}
				removed, err := roomsRepo(pool, c).RemoveBan(ctx, roomID, targetID)
				if err != nil {
					return err
				}
				if !removed {
					fmt.Printf("no active ban for %s in room %s\n", user, room)
					return nil
				}
				fmt.Printf("unbanned %s from room %s\n", user, room)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&room, "room", "", "room UUID (required)")
	cmd.Flags().StringVar(&user, "user", "", "user handle or UUID (required)")
	return cmd
}

func listBansCmd() *cobra.Command {
	var room string
	cmd := &cobra.Command{
		Use:   "list-bans",
		Short: "List active bans in a room",
		RunE: func(cmd *cobra.Command, _ []string) error {
			roomID, err := uuid.Parse(room)
			if err != nil {
				return fmt.Errorf("invalid --room (want a UUID)")
			}
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, c *cache.Cache) error {
				bans, err := roomsRepo(pool, c).ListBans(ctx, roomID)
				if err != nil {
					return err
				}
				if len(bans) == 0 {
					fmt.Println("no active bans")
					return nil
				}
				for _, b := range bans {
					exp := "permanent"
					if b.ExpiresAt != nil {
						exp = b.ExpiresAt.Format(time.RFC3339)
					}
					fmt.Printf("%s  by=%s  expires=%s\n", b.UserID, b.BannedBy, exp)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&room, "room", "", "room UUID (required)")
	return cmd
}
