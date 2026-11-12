package store

func (s *Store) DeleteMeta(key string) error {
	_, err := s.exec("DELETE FROM meta WHERE key=?", key)
	return err
}

func (s *Store) MetaPrefix(prefix string) (map[string]string, error) {
	rows, err := s.query("SELECT key,value FROM meta WHERE key LIKE ? ORDER BY key", prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}
