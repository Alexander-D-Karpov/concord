// Package config loads Concord's typed configuration from environment variables.
//
// Load populates a Config grouped into Server, Database, Auth, Voice, Logging,
// Redis, RateLimit, Storage, and Email sections using plain os.Getenv via typed
// fallback helpers — there is no config file, viper, or struct tags. Voice.Debug
// (VOICE_DEBUG) is the production-forbidden switch that lets the stress harness
// skip room-membership checks and honor the rate-limit bypass token.
package config
