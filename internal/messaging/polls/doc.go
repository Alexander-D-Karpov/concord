// Package polls implements poll creation, voting, and closing, rendered as special
// messages.
//
// Voting is a multi-step transaction (single-choice votes delete prior votes
// first) with denormalized counts recalculated in the same transaction. The poll's
// backing message is inserted through a messageInserter seam. RunCloser is a
// background loop that auto-closes expired polls.
package polls
