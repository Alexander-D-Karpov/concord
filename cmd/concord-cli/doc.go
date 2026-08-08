// Command concord-cli is a Django-manage.py-style operator tool for Concord.
//
// It connects directly to Postgres and Redis using the same configuration as the
// server (loading .env), so every command works while concord-api is running. The
// operator is treated as a superuser: mutating commands write straight to the
// database and bypass the API's gRPC auth. Commands are organized with cobra —
// interactive shells (dbshell, cacheshell), migrations (migrate, migrate status),
// user admin (user create/set-password/unlock/set-role), moderation (ban, unban,
// list-bans), room settings (settings get/set), and inspection/maintenance (stats,
// health, voice-servers, purge-messages, clear-ratelimit). Run `concord-cli help`.
package main
