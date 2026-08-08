// Package friends manages friend requests, friendships, and the block list.
//
// Friendships are stored as ordered user-ID pairs, so writes invalidate the cache
// in both directions; blocking and friendship are separate tables. The service
// decorates friends with live presence, and blocking a user triggers the injected
// KeyRotator to rotate shared-room voice keys so the blocked user loses access.
package friends
