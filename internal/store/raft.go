package store

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/raft"
)

type RaftStore struct {
	store *Store
}

func (s *Store) RaftStore() (*RaftStore, error) {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS raft_log (idx INTEGER PRIMARY KEY, body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS raft_stable (key TEXT PRIMARY KEY, value BLOB NOT NULL);`)
	return &RaftStore{store: s}, err
}

func (s *RaftStore) FirstIndex() (uint64, error) { return s.index("MIN") }
func (s *RaftStore) LastIndex() (uint64, error)  { return s.index("MAX") }

func (s *RaftStore) index(operation string) (uint64, error) {
	var index uint64
	err := s.store.db.QueryRow("SELECT COALESCE(" + operation + "(idx),0) FROM raft_log").Scan(&index)
	return index, err
}

func (s *RaftStore) GetLog(index uint64, log *raft.Log) error {
	var body []byte
	err := s.store.db.QueryRow(`SELECT body FROM raft_log WHERE idx=?`, index).Scan(&body)
	if err == sql.ErrNoRows {
		return raft.ErrLogNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(body, log)
}

func (s *RaftStore) StoreLog(log *raft.Log) error { return s.StoreLogs([]*raft.Log{log}) }

func (s *RaftStore) StoreLogs(logs []*raft.Log) error {
	tx, err := s.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, log := range logs {
		body, err := json.Marshal(log)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO raft_log(idx,body) VALUES(?,?) ON CONFLICT(idx) DO UPDATE SET body=excluded.body`, log.Index, body); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *RaftStore) DeleteRange(min, max uint64) error {
	_, err := s.store.db.Exec(`DELETE FROM raft_log WHERE idx>=? AND idx<=?`, min, max)
	return err
}

func (s *RaftStore) Set(key, value []byte) error {
	_, err := s.store.db.Exec(`INSERT INTO raft_stable(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(key), value)
	return err
}

func (s *RaftStore) Get(key []byte) ([]byte, error) {
	var value []byte
	err := s.store.db.QueryRow(`SELECT value FROM raft_stable WHERE key=?`, string(key)).Scan(&value)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return value, err
}

func (s *RaftStore) SetUint64(key []byte, value uint64) error {
	var body [8]byte
	binary.BigEndian.PutUint64(body[:], value)
	return s.Set(key, body[:])
}

func (s *RaftStore) GetUint64(key []byte) (uint64, error) {
	value, err := s.Get(key)
	if err != nil || len(value) == 0 {
		return 0, err
	}
	if len(value) != 8 {
		return 0, fmt.Errorf("invalid persisted raft integer")
	}
	return binary.BigEndian.Uint64(value), nil
}
