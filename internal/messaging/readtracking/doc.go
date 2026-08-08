// Package readtracking tracks the last-read message and unread counts for rooms
// and DMs, and broadcasts unread updates.
//
// Room and DM have fully parallel method sets — pick the right surface. Last-read
// is stored as an int64 Snowflake message ID and compared numerically to derive
// unread counts, which relies on Snowflakes being time-ordered. MarkRoomAsRead and
// MarkDMAsRead return the new last-read ID and unread count and broadcast the
// change as a side effect.
package readtracking
