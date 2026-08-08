package room

import (
	"sync"

	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
)

// Index is a legacy user/SSRC -> session lookup guarded by an RWMutex. It is a
// refactor leftover (see the package doc): the live routing index lives in
// session.Manager, and this type is not populated in the running server.
type Index struct {
	userToSession map[string]*session.Session
	ssrcToSession map[uint32]*session.Session
	mu            sync.RWMutex
}

// NewIndex returns an empty Index. Retained for legacy callers only.
func NewIndex() *Index {
	return &Index{
		userToSession: make(map[string]*session.Session),
		ssrcToSession: make(map[uint32]*session.Session),
	}
}

// AddSession indexes sess by both its UserID and SSRC, replacing any prior entry
// under those keys. Concurrency-safe.
func (i *Index) AddSession(sess *session.Session) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.userToSession[sess.UserID] = sess
	i.ssrcToSession[sess.SSRC] = sess
}

// RemoveSession deletes sess's UserID and SSRC entries. It keys off the passed
// session's fields, so it must be called with the same values that were indexed.
func (i *Index) RemoveSession(sess *session.Session) {
	i.mu.Lock()
	defer i.mu.Unlock()

	delete(i.userToSession, sess.UserID)
	delete(i.ssrcToSession, sess.SSRC)
}

// GetByUser returns the session for userID, or nil if none is indexed.
func (i *Index) GetByUser(userID string) *session.Session {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.userToSession[userID]
}

// GetBySSRC returns the session for ssrc, or nil if none is indexed.
func (i *Index) GetBySSRC(ssrc uint32) *session.Session {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.ssrcToSession[ssrc]
}
