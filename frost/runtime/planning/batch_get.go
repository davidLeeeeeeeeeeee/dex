package planning

type bulkStateReader interface {
	GetMany(keys []string) (map[string][]byte, error)
}

func getMany(reader ChainStateReader, keys []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(keys))
	if len(keys) == 0 {
		return out, nil
	}

	uniq := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		uniq = append(uniq, key)
	}

	if bulkReader, ok := reader.(bulkStateReader); ok {
		return bulkReader.GetMany(uniq)
	}

	for _, key := range uniq {
		v, exists, err := reader.Get(key)
		if err != nil {
			return nil, err
		}
		if exists {
			out[key] = v
		}
	}
	return out, nil
}
