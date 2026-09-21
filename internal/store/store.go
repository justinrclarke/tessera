package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"tessera/internal/api"
	"tessera/internal/policy"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS apps (name TEXT PRIMARY KEY, body TEXT NOT NULL, generation INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS app_history (name TEXT NOT NULL, generation INTEGER NOT NULL, body TEXT NOT NULL, PRIMARY KEY (name, generation));
CREATE TABLE IF NOT EXISTS nodes (id TEXT PRIMARY KEY, body TEXT NOT NULL, last_seen INTEGER NOT NULL, status TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS assignments (id TEXT PRIMARY KEY, app TEXT NOT NULL, node_id TEXT NOT NULL, body TEXT NOT NULL, status TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS actions (id TEXT PRIMARY KEY, at INTEGER NOT NULL, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS routes (name TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS configs (name TEXT NOT NULL, kind TEXT NOT NULL, body TEXT NOT NULL, PRIMARY KEY (name, kind));
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS snapshots (idx INTEGER PRIMARY KEY, body TEXT NOT NULL, sig TEXT NOT NULL);
`)
	return err
}

func (s *Store) PutApp(a api.App) error {
	prev, err := s.GetApp(a.Name)
	if err == nil && !prev.ReleaseEqual(a) {
		if a.Generation <= prev.Generation {
			a.Generation = prev.Generation + 1
		}
		a.HealthyGeneration = prev.HealthyGeneration
		if err := s.putHistory(prev); err != nil {
			return err
		}
	} else if err == nil {
		if a.Generation == 0 {
			a.Generation = prev.Generation
		}
		if a.HealthyGeneration == 0 {
			a.HealthyGeneration = prev.HealthyGeneration
		}
	}
	if a.Generation == 0 {
		a.Generation = 1
	}
	return s.putJSON("apps", "name", a.Name, a, a.Generation)
}

func (s *Store) putHistory(a api.App) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO app_history(name, generation, body) VALUES(?, ?, ?)
		ON CONFLICT(name, generation) DO UPDATE SET body=excluded.body`, a.Name, a.Generation, string(b))
	return err
}

func (s *Store) History(name string, generation int64) (api.App, error) {
	var body string
	err := s.db.QueryRow(`SELECT body FROM app_history WHERE name=? AND generation=?`, name, generation).Scan(&body)
	if err != nil {
		return api.App{}, err
	}
	var a api.App
	err = json.Unmarshal([]byte(body), &a)
	return a, err
}

func (s *Store) GetApp(name string) (api.App, error) {
	return one[api.App](s, `SELECT body FROM apps WHERE name=?`, name)
}

func (s *Store) ListApps() ([]api.App, error) {
	return list[api.App](s, `SELECT body FROM apps ORDER BY name`)
}

func (s *Store) MarkHealthy(name string, generation int64) error {
	a, err := s.GetApp(name)
	if err != nil {
		return err
	}
	if generation < a.HealthyGeneration {
		return nil
	}
	a.HealthyGeneration = generation
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE apps SET body=?, generation=? WHERE name=?`, string(b), a.Generation, name)
	return err
}

func (s *Store) Rollback(name string) (api.App, error) {
	cur, err := s.GetApp(name)
	if err != nil {
		return api.App{}, err
	}
	if cur.HealthyGeneration == 0 || cur.HealthyGeneration >= cur.Generation {
		return api.App{}, fmt.Errorf("no healthy generation for %s", name)
	}
	old, err := s.History(name, cur.HealthyGeneration)
	if err != nil {
		return api.App{}, err
	}
	old.Generation = cur.HealthyGeneration
	old.HealthyGeneration = cur.HealthyGeneration
	b, err := json.Marshal(old)
	if err != nil {
		return api.App{}, err
	}
	_, err = s.db.Exec(`UPDATE apps SET body=?, generation=? WHERE name=?`, string(b), old.Generation, name)
	return old, err
}

func (s *Store) PutNode(n api.Node) error {
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO nodes(id, body, last_seen, status) VALUES(?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET body=excluded.body, last_seen=excluded.last_seen, status=excluded.status`,
		n.ID, string(b), n.LastSeen.UnixMilli(), n.Status)
	return err
}

func (s *Store) GetNode(id string) (api.Node, error) {
	return one[api.Node](s, `SELECT body FROM nodes WHERE id=?`, id)
}

func (s *Store) ListNodes() ([]api.Node, error) {
	return list[api.Node](s, `SELECT body FROM nodes ORDER BY id`)
}

func (s *Store) PutAssignment(a api.Assignment) error {
	if a.Updated.IsZero() {
		a.Updated = time.Now()
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO assignments(id, app, node_id, body, status) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET app=excluded.app, node_id=excluded.node_id, body=excluded.body, status=excluded.status`,
		a.ID, a.App, a.NodeID, string(b), a.Status)
	return err
}

func (s *Store) GetAssignment(id string) (api.Assignment, error) {
	return one[api.Assignment](s, `SELECT body FROM assignments WHERE id=?`, id)
}

func (s *Store) ListAssignments() ([]api.Assignment, error) {
	return list[api.Assignment](s, `SELECT body FROM assignments ORDER BY id`)
}

func (s *Store) AddAction(a api.Action) error {
	if a.At.IsZero() {
		a.At = time.Now()
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO actions(id, at, body) VALUES(?, ?, ?)`, a.ID, a.At.UnixMilli(), string(b))
	return err
}

func (s *Store) ListActions(limit int) ([]api.Action, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT body FROM actions ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.Action
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var a api.Action
		if err := json.Unmarshal([]byte(body), &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) HasAction(kind, target, reason string) (bool, error) {
	rows, err := s.db.Query(`SELECT body FROM actions ORDER BY at DESC LIMIT 200`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return false, err
		}
		var a api.Action
		if err := json.Unmarshal([]byte(body), &a); err != nil {
			return false, err
		}
		if a.Kind == kind && a.Target == target && a.Reason == reason {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) PutRoute(r api.Route) error {
	return s.putJSON("routes", "name", r.Name, r, 0)
}

func (s *Store) ListRoutes() ([]api.Route, error) {
	return list[api.Route](s, `SELECT body FROM routes ORDER BY name`)
}

func (s *Store) PutConfig(c api.Config) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO configs(name, kind, body) VALUES(?, ?, ?)
		ON CONFLICT(name, kind) DO UPDATE SET body=excluded.body`, c.Name, api.KindConfig, string(b))
	return err
}

func (s *Store) PutSecret(sec api.Secret) error {
	b, err := json.Marshal(sec)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO configs(name, kind, body) VALUES(?, ?, ?)
		ON CONFLICT(name, kind) DO UPDATE SET body=excluded.body`, sec.Name, api.KindSecret, string(b))
	return err
}

func (s *Store) GetConfig(name string) (api.Config, error) {
	var body string
	err := s.db.QueryRow(`SELECT body FROM configs WHERE name=? AND kind=?`, name, api.KindConfig).Scan(&body)
	if err != nil {
		return api.Config{}, err
	}
	var c api.Config
	err = json.Unmarshal([]byte(body), &c)
	return c, err
}

func (s *Store) GetSecret(name string) (api.Secret, error) {
	var body string
	err := s.db.QueryRow(`SELECT body FROM configs WHERE name=? AND kind=?`, name, api.KindSecret).Scan(&body)
	if err != nil {
		return api.Secret{}, err
	}
	var sec api.Secret
	err = json.Unmarshal([]byte(body), &sec)
	return sec, err
}

func (s *Store) ListConfigs() ([]api.Config, error) {
	return listKind[api.Config](s, api.KindConfig)
}

func (s *Store) ListSecrets() ([]api.Secret, error) {
	return listKind[api.Secret](s, api.KindSecret)
}

func (s *Store) SetPolicy(p api.Policy) error {
	p = policy.Normalize(p)
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.SetMeta("policy", string(b))
}

func (s *Store) Policy() (api.Policy, error) {
	v, err := s.Meta("policy")
	if err != nil || v == "" {
		return policy.Default(), nil
	}
	var p api.Policy
	if err := json.Unmarshal([]byte(v), &p); err != nil {
		return api.Policy{}, err
	}
	return policy.Normalize(p), nil
}

func (s *Store) Meta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) AppendSnapshot(snap api.Snapshot, sig string) error {
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return s.AppendSnapshotRaw(snap.Index, string(b), sig)
}

func (s *Store) AppendSnapshotRaw(idx uint64, body, sig string) error {
	_, err := s.db.Exec(`INSERT INTO snapshots(idx, body, sig) VALUES(?, ?, ?)`, idx, body, sig)
	return err
}

func (s *Store) RawSnapshot() (string, string, error) {
	var body, sig string
	err := s.db.QueryRow(`SELECT body, sig FROM snapshots ORDER BY idx DESC LIMIT 1`).Scan(&body, &sig)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return body, sig, err
}

func (s *Store) LatestSnapshot() (api.Snapshot, string, error) {
	var body, sig string
	err := s.db.QueryRow(`SELECT body, sig FROM snapshots ORDER BY idx DESC LIMIT 1`).Scan(&body, &sig)
	if err == sql.ErrNoRows {
		return api.Snapshot{}, "", nil
	}
	if err != nil {
		return api.Snapshot{}, "", err
	}
	var snap api.Snapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		return api.Snapshot{}, "", err
	}
	return snap, sig, nil
}

func (s *Store) LoadSnapshot(snap api.Snapshot, epoch uint64) error {
	for _, a := range snap.Apps {
		b, err := json.Marshal(a)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`INSERT INTO apps(name, body, generation) VALUES(?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET body=excluded.body, generation=excluded.generation`, a.Name, string(b), a.Generation); err != nil {
			return err
		}
	}
	for _, asg := range snap.Assignments {
		asg.Epoch = epoch
		if err := s.PutAssignment(asg); err != nil {
			return err
		}
	}
	for _, r := range snap.Routes {
		if err := s.PutRoute(r); err != nil {
			return err
		}
	}
	for _, c := range snap.Configs {
		if err := s.PutConfig(c); err != nil {
			return err
		}
	}
	for _, sec := range snap.Secrets {
		if err := s.PutSecret(sec); err != nil {
			return err
		}
	}
	return s.SetPolicy(snap.Policy)
}

func (s *Store) Backup(path string) error {
	q := fmt.Sprintf("VACUUM INTO '%s'", escape(path))
	_, err := s.db.Exec(q)
	return err
}

func (s *Store) putJSON(table, keyCol, key string, v any, generation int64) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	switch table {
	case "apps":
		_, err = s.db.Exec(`INSERT INTO apps(name, body, generation) VALUES(?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET body=excluded.body, generation=excluded.generation`, key, string(b), generation)
	case "routes":
		_, err = s.db.Exec(`INSERT INTO routes(name, body) VALUES(?, ?) ON CONFLICT(name) DO UPDATE SET body=excluded.body`, key, string(b))
	default:
		return fmt.Errorf("unknown table %s", table)
	}
	return err
}

func one[T any](s *Store, q string, args ...any) (T, error) {
	var zero T
	var body string
	err := s.db.QueryRow(q, args...).Scan(&body)
	if err != nil {
		return zero, err
	}
	err = json.Unmarshal([]byte(body), &zero)
	return zero, err
}

func list[T any](s *Store, q string, args ...any) ([]T, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func listKind[T any](s *Store, kind string) ([]T, error) {
	return list[T](s, `SELECT body FROM configs WHERE kind=? ORDER BY name`, kind)
}

func escape(path string) string {
	out := make([]byte, 0, len(path))
	for i := 0; i < len(path); i++ {
		if path[i] == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, path[i])
	}
	return string(out)
}
