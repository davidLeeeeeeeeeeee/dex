package db

func SaveClientInfo(mgr *Manager, info *ClientInfo) error {
	key := "clientinfo_" + info.Ip
	data, err := ProtoMarshal(info)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))
	return nil
}

func GetClientInfo(mgr *Manager, ip string) (*ClientInfo, error) {
	key := "clientinfo_" + ip
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
