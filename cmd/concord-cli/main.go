package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/joho/godotenv"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load(".env")

	clearCmd := flag.NewFlagSet("clear-ratelimit", flag.ExitOnError)
	clearAll := clearCmd.Bool("all", false, "clear all rate limits")
	clearKey := clearCmd.String("key", "", "clear specific rate limit key")

	if len(os.Args) < 2 {
		printUsage()
		return nil
	}

	switch os.Args[1] {
	case "clear-ratelimit":
		err := clearCmd.Parse(os.Args[2:])
		if err != nil {
			return err
		}
		return handleClearRateLimit(*clearAll, *clearKey)
	default:
		printUsage()
		return nil
	}
}

func handleClearRateLimit(all bool, key string) error {
	if !all && key == "" {
		return fmt.Errorf("must specify either --all or --key")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if !cfg.Redis.Enabled {
		return fmt.Errorf("redis is not enabled in config")
	}

	cacheClient, err := cache.New(
		cfg.Redis.Host,
		cfg.Redis.Port,
		cfg.Redis.Password,
		cfg.Redis.DB,
	)
	if err != nil {
		return fmt.Errorf("connect to redis: %w", err)
	}
	defer func(cacheClient *cache.Cache) {
		err := cacheClient.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error closing cache client: %v\n", err)
		}
	}(cacheClient)

	ctx := context.Background()

	if all {
		if err := clearAllRateLimits(ctx, cacheClient); err != nil {
			return fmt.Errorf("clear all rate limits: %w", err)
		}
		fmt.Println("All rate limits cleared")
		return nil
	}

	cacheKey := fmt.Sprintf("ratelimit:%s", key)
	if err := cacheClient.Delete(ctx, cacheKey); err != nil {
		return fmt.Errorf("clear rate limit: %w", err)
	}
	fmt.Printf("Rate limit cleared for key: %s\n", key)
	return nil
}

func clearAllRateLimits(ctx context.Context, c *cache.Cache) error {
	pattern := "ratelimit:*"
	iter := c.Client().Scan(ctx, 0, pattern, 0).Iterator()
	pipe := c.Client().Pipeline()

	count := 0
	for iter.Next(ctx) {
		pipe.Del(ctx, iter.Val())
		count++
	}

	if err := iter.Err(); err != nil {
		return err
	}

	if count == 0 {
		fmt.Println("No rate limit keys found")
		return nil
	}

	_, err := pipe.Exec(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("Cleared %d rate limit keys\n", count)
	return nil
}

func printUsage() {
	fmt.Println("Concord CLI")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  concord-cli clear-ratelimit --all")
	fmt.Println("  concord-cli clear-ratelimit --key <key>")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  concord-cli clear-ratelimit --all")
	fmt.Println("  concord-cli clear-ratelimit --key auth")
	fmt.Println("  concord-cli clear-ratelimit --key user:123456")
}
