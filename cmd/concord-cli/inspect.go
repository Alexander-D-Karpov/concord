package main

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/Alexander-D-Karpov/concord/internal/infra/db"
	"github.com/Alexander-D-Karpov/concord/internal/registry"
	"github.com/Alexander-D-Karpov/concord/internal/retention"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// statsCmd prints high-level counts of core entities.
func statsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show counts of users, rooms, messages, and voice servers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, _ *cache.Cache) error {
				counts := []struct {
					label string
					query string
				}{
					{"users", `SELECT count(*) FROM users`},
					{"rooms", `SELECT count(*) FROM rooms WHERE deleted_at IS NULL`},
					{"messages", `SELECT count(*) FROM messages WHERE deleted_at IS NULL`},
					{"voice servers", `SELECT count(*) FROM voice_servers`},
				}
				for _, c := range counts {
					var n int64
					if err := pool.QueryRow(ctx, c.query).Scan(&n); err != nil {
						return fmt.Errorf("count %s: %w", c.label, err)
					}
					fmt.Printf("%-14s %d\n", c.label, n)
				}
				return nil
			})
		},
	}
}

// healthCmd pings Postgres and (if enabled) Redis, reporting each.
func healthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check connectivity to Postgres and Redis",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			ctx := context.Background()
			ok := true

			database, err := db.New(cfg.Database)
			if err != nil {
				fmt.Printf("postgres  ERROR  %v\n", err)
				ok = false
			} else {
				defer database.Close()
				if err := database.Health(ctx); err != nil {
					fmt.Printf("postgres  ERROR  %v\n", err)
					ok = false
				} else {
					fmt.Println("postgres  OK")
				}
			}

			if !cfg.Redis.Enabled {
				fmt.Println("redis     DISABLED")
			} else if c, err := cache.New(cfg.Redis.Host, cfg.Redis.Port, cfg.Redis.Password, cfg.Redis.DB); err != nil {
				fmt.Printf("redis     ERROR  %v\n", err)
				ok = false
			} else {
				defer func() { _ = c.Close() }()
				if err := c.Ping(ctx); err != nil {
					fmt.Printf("redis     ERROR  %v\n", err)
					ok = false
				} else {
					fmt.Println("redis     OK")
				}
			}

			if !ok {
				return fmt.Errorf("one or more components are unhealthy")
			}
			return nil
		},
	}
}

// voiceServersCmd lists the voice servers registered with the API.
func voiceServersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "voice-servers",
		Short: "List registered voice servers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, _ *cache.Cache) error {
				servers, err := registry.NewService(pool, zap.NewNop()).ListServers(ctx, nil)
				if err != nil {
					return err
				}
				if len(servers) == 0 {
					fmt.Println("no voice servers registered")
					return nil
				}
				for _, s := range servers {
					fmt.Printf("%s  region=%s  udp=%s  status=%s  load=%.2f\n",
						s.ID, s.Region, s.AddrUDP, s.Status, s.LoadScore)
				}
				return nil
			})
		},
	}
}

// purgeMessagesCmd runs the retention purge once and reports how many messages were
// soft-deleted.
func purgeMessagesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "purge-messages",
		Short: "Run message retention now (soft-delete expired messages)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, _ *cache.Cache) error {
				n, err := retention.NewService(pool, zap.NewNop()).PurgeOnce(ctx)
				if err != nil {
					return err
				}
				fmt.Printf("purged %d messages\n", n)
				return nil
			})
		},
	}
}
