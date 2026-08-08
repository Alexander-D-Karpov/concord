package main

import (
	"context"
	"fmt"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/spf13/cobra"
)

// clearRateLimitCmd deletes rate-limit state from Redis: every "ratelimit:*" key
// with --all, or a single "ratelimit:<key>" with --key.
func clearRateLimitCmd() *cobra.Command {
	var all bool
	var key string
	cmd := &cobra.Command{
		Use:   "clear-ratelimit",
		Short: "Clear rate-limit state from Redis",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !all && key == "" {
				return fmt.Errorf("must specify either --all or --key")
			}
			return withCache(func(ctx context.Context, _ *config.Config, c *cache.Cache) error {
				if all {
					n, err := clearAllRateLimits(ctx, c)
					if err != nil {
						return err
					}
					fmt.Printf("cleared %d rate-limit keys\n", n)
					return nil
				}
				if err := c.Delete(ctx, fmt.Sprintf("ratelimit:%s", key)); err != nil {
					return err
				}
				fmt.Printf("rate limit cleared for key: %s\n", key)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "clear all rate limits")
	cmd.Flags().StringVar(&key, "key", "", "clear a specific rate-limit key")
	return cmd
}

// clearAllRateLimits scans for every "ratelimit:*" key and deletes them in a single
// pipeline, returning how many were removed.
func clearAllRateLimits(ctx context.Context, c *cache.Cache) (int, error) {
	iter := c.Client().Scan(ctx, 0, "ratelimit:*", 0).Iterator()
	pipe := c.Client().Pipeline()
	count := 0
	for iter.Next(ctx) {
		pipe.Del(ctx, iter.Val())
		count++
	}
	if err := iter.Err(); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return count, nil
}
