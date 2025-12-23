package session

func (m *Manager) GetAllSessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

func (m *Manager) GetAllRooms() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]string, 0, len(m.roomMap))
	for roomID := range m.roomMap {
		out = append(out, roomID)
	}
	return out
}

func (m *Manager) GetByAddrString(addr string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.addrMap[addr]
}
