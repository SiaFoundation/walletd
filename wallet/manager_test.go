package wallet

// SyncPool forces a sync of the transaction pool for testing
// purposes.
func (m *Manager) SyncPool() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetPool()
}
