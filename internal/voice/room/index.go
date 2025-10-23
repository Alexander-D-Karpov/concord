package room

import (
	"sync"

	"github.com/Alexander-D-Karpov/concord/internal/voice/session"
)

type Index struct {
	userToSession map[string]*session.Session
	ssrcToSession map[uint32]*session.Session
	mu            sync.RWMutex
}

func NewIndex() *Index {
	return &Index{
		userToSession: make(map[string]*session.Session),
		ssrcToSession: make(map[uint32]*session.Session),
	}
}

func (i *Index) AddSession(sess *session.Session) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.userToSession[sess.UserID] = sess
	i.ssrcToSession[sess.SSRC] = sess
}

func (i *Index) RemoveSession(sess *session.Session) {
	i.mu.Lock()
	defer i.mu.Unlock()

	delete(i.userToSession, sess.UserID)
	delete(i.ssrcToSession, sess.SSRC)
}

func (i *Index) GetByUser(userID string) *session.Session {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.userToSession[userID]
}

func (i *Index) GetBySSRC(ssrc uint32) *session.Session {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.ssrcToSession[ssrc]
}
