package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Alexander-D-Karpov/concord/internal/common/config"
	"github.com/Alexander-D-Karpov/concord/internal/infra/cache"
	"github.com/Alexander-D-Karpov/concord/internal/infra/db"
	"github.com/Alexander-D-Karpov/concord/internal/rooms"
	"github.com/Alexander-D-Karpov/concord/internal/users"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// loadConfig loads .env (best-effort) and returns the parsed application config, so
// the CLI connects to exactly the same Postgres/Redis the server uses.
func loadConfig() (*config.Config, error) {
	_ = godotenv.Load(".env")
	return config.Load()
}

// withDB loads config, opens the database pool (via the same db.New the server uses)
// and — when Redis is enabled — the cache, runs fn, and closes both. The cache is
// passed so mutating commands can build cache-backed repositories that invalidate
// the same keys the running server reads; it is nil when Redis is disabled or
// unreachable.
func withDB(fn func(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, c *cache.Cache) error) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	database, err := db.New(cfg.Database)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer database.Close()

	var c *cache.Cache
	if cfg.Redis.Enabled {
		cc, cerr := cache.New(cfg.Redis.Host, cfg.Redis.Port, cfg.Redis.Password, cfg.Redis.DB)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "warning: redis unavailable, cache invalidation disabled: %v\n", cerr)
		} else {
			c = cc
			defer func() { _ = c.Close() }()
		}
	}
	return fn(context.Background(), cfg, database.Pool, c)
}

// withCache loads config, opens the Redis client, runs fn, and closes it. It errors
// if Redis is disabled in config.
func withCache(fn func(ctx context.Context, cfg *config.Config, c *cache.Cache) error) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Redis.Enabled {
		return fmt.Errorf("redis is not enabled in config")
	}
	c, err := cache.New(cfg.Redis.Host, cfg.Redis.Port, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		return fmt.Errorf("connect to redis: %w", err)
	}
	defer func() { _ = c.Close() }()
	return fn(context.Background(), cfg, c)
}

// roomsRepo builds a rooms repository, cache-backed when a cache is available so
// mutations invalidate the shared cache the running server reads.
func roomsRepo(pool *pgxpool.Pool, c *cache.Cache) *rooms.Repository {
	if c != nil {
		return rooms.NewRepositoryWithCache(pool, c)
	}
	return rooms.NewRepository(pool)
}

// usersRepo builds a users repository, cache-backed when a cache is available.
func usersRepo(pool *pgxpool.Pool, c *cache.Cache) *users.Repository {
	if c != nil {
		return users.NewRepositoryWithCache(pool, c)
	}
	return users.NewRepository(pool)
}
