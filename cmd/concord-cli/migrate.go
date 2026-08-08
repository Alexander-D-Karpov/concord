package main

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/Alexander-D-Karpov/concord/internal/infra/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// migrateCmd runs pending migrations and hosts the `status` subcommand.
func migrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, _ *cache.Cache) error {
				if err := migrations.Run(ctx, pool); err != nil {
					return err
				}
				fmt.Println("migrations applied")
				return nil
			})
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show applied and pending migrations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withDB(func(ctx context.Context, _ *config.Config, pool *pgxpool.Pool, _ *cache.Cache) error {
				statuses, err := migrations.Status(ctx, pool)
				if err != nil {
					return err
				}
				pending := 0
				for _, s := range statuses {
					mark := "applied"
					if !s.Applied {
						mark = "PENDING"
						pending++
					}
					fmt.Printf("%3d  %-8s  %s\n", s.Version, mark, s.Name)
				}
				fmt.Printf("\n%d migrations, %d pending\n", len(statuses), pending)
				return nil
			})
		},
	})
	return cmd
}
