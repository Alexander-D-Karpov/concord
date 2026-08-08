package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache is a thin JSON-serializing wrapper around a Redis client, exposing the
// subset of Redis operations the app uses.
type Cache struct {
	client *redis.Client
}

// New opens a Redis client to host:port (selecting logical DB db) with fixed
// timeouts and pool sizing, then verifies connectivity with a 5s Ping. It
// returns an error if the ping fails.
func New(host string, port int, password string, db int) (*Cache, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         fmt.Sprintf("%s:%d", host, port),
		Password:     password,
		DB:           db,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     10,
		MinIdleConns: 5,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	return &Cache{client: client}, nil
}

// Client exposes the underlying Redis client for operations not wrapped here.
func (c *Cache) Client() *redis.Client {
	return c.client
}

// Set JSON-marshals value and stores it under key with the given ttl (0 means no
// expiry). It returns an error if value cannot be marshaled.
func (c *Cache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal value: %w", err)
	}

	return c.client.Set(ctx, key, data, ttl).Err()
}

// Get fetches key and JSON-unmarshals it into dest. It returns the ErrCacheMiss
// sentinel (compare with errors.Is) when the key is absent, so a miss is
// distinguishable from a transport error.
func (c *Cache) Get(ctx context.Context, key string, dest interface{}) error {
	data, err := c.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return ErrCacheMiss
	}
	if err != nil {
		return err
	}

	return json.Unmarshal(data, dest)
}

// Delete removes the given keys; absent keys are ignored (not an error).
func (c *Cache) Delete(ctx context.Context, keys ...string) error {
	return c.client.Del(ctx, keys...).Err()
}

// Exists reports whether key is present.
func (c *Cache) Exists(ctx context.Context, key string) (bool, error) {
	result, err := c.client.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return result > 0, nil
}

// SetNX atomically sets key to the JSON encoding of value with ttl only if key
// does not already exist. The bool reports whether the write happened; false
// means the key was already present. Useful as a distributed lock/dedupe.
func (c *Cache) SetNX(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return false, fmt.Errorf("marshal value: %w", err)
	}

	return c.client.SetNX(ctx, key, data, ttl).Result()
}

// Incr atomically increments the integer at key (creating it as 0 first) and
// returns the new value. Used for counters such as rate limits.
func (c *Cache) Incr(ctx context.Context, key string) (int64, error) {
	return c.client.Incr(ctx, key).Result()
}

// Expire sets or refreshes the ttl of an existing key.
func (c *Cache) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return c.client.Expire(ctx, key, ttl).Err()
}

// Ping verifies the Redis connection is alive.
func (c *Cache) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

// Close releases the Redis client and its connection pool.
func (c *Cache) Close() error {
	return c.client.Close()
}

// FlushAll deletes every key in the selected Redis database. Destructive;
// intended for tests and local resets.
func (c *Cache) FlushAll(ctx context.Context) error {
	return c.client.FlushAll(ctx).Err()
}

// ErrCacheMiss is returned by Get when a key is absent, letting callers tell a
// miss apart from a real error via errors.Is.
var ErrCacheMiss = fmt.Errorf("cache miss")

// DeletePattern deletes all keys matching the Redis glob pattern. It SCANs the
// keyspace and DELs matches through a pipeline, so it is not atomic: keys may be
// created or deleted concurrently during the scan.
func (c *Cache) DeletePattern(ctx context.Context, pattern string) error {
	iter := c.client.Scan(ctx, 0, pattern, 0).Iterator()
	pipe := c.client.Pipeline()

	for iter.Next(ctx) {
		pipe.Del(ctx, iter.Val())
	}

	if err := iter.Err(); err != nil {
		return err
	}

	_, err := pipe.Exec(ctx)
	return err
}

// HSet writes all field/value pairs into the hash at key and sets the key's ttl,
// pipelining the HSETs and the EXPIRE into one round trip.
func (c *Cache) HSet(ctx context.Context, key string, values map[string]string, ttl time.Duration) error {
	pipe := c.client.Pipeline()
	for k, v := range values {
		pipe.HSet(ctx, key, k, v)
	}
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// HGetAll returns all field/value pairs of the hash at key; an absent key yields
// an empty (non-nil) map with no error.
func (c *Cache) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return c.client.HGetAll(ctx, key).Result()
}
