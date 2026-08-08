// Package dm implements direct-message channels — 1:1 messaging plus DM voice
// calls — mirroring most of the chat feature set for private conversations.
//
// It uses two repositories: Repository for channels and calls, and
// MessageRepository for messages — do not assume a single one. The handler takes
// read-tracking and typing services through optional setters (nil-guarded). DM
// calls are set up through voiceassign.
package dm
