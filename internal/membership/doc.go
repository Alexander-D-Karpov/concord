// Package membership holds the business logic for room invites and membership
// changes (invite, accept, reject, remove, set-role, nickname).
//
// It has no repository of its own — it persists through rooms.Repository — so it
// must invalidate the rooms membership cache after mutations. Removing a member
// calls an injected KeyRotator to rotate the room's voice encryption key so the
// removed member can no longer decrypt future media; other membership changes
// (invite/accept/reject/set-role/nickname) do not rotate.
package membership
