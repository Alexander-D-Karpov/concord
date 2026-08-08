// Package slowmode enforces per-room slow mode by storing the interval and
// rate-limiting senders.
//
// CheckAndStamp both checks the cooldown and records the send timestamp, so
// calling it twice for one message double-stamps; it returns the remaining seconds
// when a send is blocked. An exempt role (moderator/admin) bypasses the limit. The
// interval is cached via the cache-aside pattern and invalidated on Set.
package slowmode
