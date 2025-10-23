package db

// SaveClientInfo 改为成员函数
func (mgr *Manager) SaveClientInfo(info *ClientInfo) error {
	key := KeyClientInfo(info.Ip)
	data, err := ProtoMarshal(info)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))
	return nil
}

// GetClientInfo 改为成员函数
func (mgr *Manager) GetClientInfo(ip string) (*ClientInfo, error) {
	key := KeyClientInfo(ip)
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	info := &ClientInfo{}
	if err := ProtoUnmarshal([]byte(val), info); err != nil {
		return nil, err
	}
	return info, nil
}
