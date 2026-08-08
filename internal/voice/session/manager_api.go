package session

// GetAllSessions returns a freshly allocated slice of every live session, in
// unspecified order. Intended for admin/debug APIs, not the hot path.
func (m *Manager) GetAllSessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// GetAllRooms returns the IDs of all non-empty rooms, in unspecified order.
func (m *Manager) GetAllRooms() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]string, 0, len(m.roomMap))
	for roomID := range m.roomMap {
		out = append(out, roomID)
	}
	return out
}

// GetByAddrString looks a session up by its address string key (the form
// net.UDPAddr.String() produces), or returns nil. Convenience for callers that
// already hold the string key.
func (m *Manager) GetByAddrString(addr string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.addrMap[addr]
}
