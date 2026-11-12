package store

func (s *Store) ImageReferences() (map[string]bool, error) {
	rows, err := s.query(`SELECT json_extract(body,'$.image') FROM apps
UNION SELECT json_extract(body,'$.image') FROM app_history
UNION SELECT json_extract(body,'$.image') FROM assignments WHERE status NOT IN ('stopped','succeeded')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[string]bool{}
	for rows.Next() {
		var ref *string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		if ref != nil && *ref != "" {
			refs[*ref] = true
		}
	}
	return refs, rows.Err()
}
