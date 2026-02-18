package db

// GetState reads one key from StateDB mirror.
// It returns (nil, false, nil) when StateDB is disabled.
func (manager *Manager) GetState(key string) ([]byte, bool, error) {
	manager.mu.RLock()
	stateStore := manager.stateDB
	manager.mu.RUnlock()
	if stateStore == nil {
		return nil, false, nil
	}
	return stateStore.Get(key)
}
