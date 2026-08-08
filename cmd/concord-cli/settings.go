package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// settingsCmd groups the room-settings subcommands.
func settingsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "settings", Short: "Get or set room settings"}
	cmd.AddCommand(settingsGetCmd(), settingsSetCmd())
	return cmd
}

func settingsGetCmd() *cobra.Command {
	var room string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Print a room's settings as JSON",
		RunE: func(cmd *cobra.Command, _ []string) error {
			roomID, err := uuid.Parse(room)
			if err != nil {
				return fmt.Errorf("invalid --room (want a UUID)")
			}
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, c *cache.Cache) error {
				s, err := roomsRepo(pool, c).GetSettings(ctx, roomID)
				if err != nil {
					return err
				}
				out, err := json.MarshalIndent(s, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(out))
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&room, "room", "", "room UUID (required)")
	return cmd
}

func settingsSetCmd() *cobra.Command {
	var room, jsonStr string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Update room settings from JSON (merged onto current settings)",
		Long: "Update room settings. The JSON is merged onto the current settings, so you only\n" +
			"need to include the fields you want to change. Field names match `settings get`\n" +
			"output, e.g. --json '{\"WhoCanPost\":\"moderator\",\"MemberCap\":50}'.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			roomID, err := uuid.Parse(room)
			if err != nil {
				return fmt.Errorf("invalid --room (want a UUID)")
			}
			if jsonStr == "" {
				return fmt.Errorf("--json is required")
			}
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, c *cache.Cache) error {
				repo := roomsRepo(pool, c)
				cur, err := repo.GetSettings(ctx, roomID)
				if err != nil {
					return err
				}
				// Merge: unmarshal the provided fields onto the current settings.
				if err := json.Unmarshal([]byte(jsonStr), &cur); err != nil {
					return fmt.Errorf("parse --json: %w", err)
				}
				if cur.WhoCanInvite != "member" && cur.WhoCanInvite != "moderator" {
					return fmt.Errorf("WhoCanInvite must be 'member' or 'moderator'")
				}
				if cur.WhoCanPost != "member" && cur.WhoCanPost != "moderator" {
					return fmt.Errorf("WhoCanPost must be 'member' or 'moderator'")
				}
				// Clamp negatives (mirrors the admin service, which the CLI bypasses).
				if cur.SlowModeInterval < 0 {
					cur.SlowModeInterval = 0
				}
				if cur.MemberCap < 0 {
					cur.MemberCap = 0
				}
				if cur.RetentionDays < 0 {
					cur.RetentionDays = 0
				}
				if err := repo.UpdateSettings(ctx, roomID, cur); err != nil {
					return err
				}
				out, _ := json.MarshalIndent(cur, "", "  ")
				fmt.Println(string(out))
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&room, "room", "", "room UUID (required)")
	cmd.Flags().StringVar(&jsonStr, "json", "", "settings fields to change, as JSON (required)")
	return cmd
}
