package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Statement struct {
	SQL  string          `json:"sql"`
	Args json.RawMessage `json:"args"`
}

type Batch struct {
	Revision   uint64      `json:"revision"`
	Statements []Statement `json:"statements"`
}

var ErrStaleBatch = errors.New("replicated command was prepared against an older state")

func (s *Store) Capture(fn func(*Store) error) (Batch, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Batch{}, err
	}
	defer tx.Rollback()
	var batch Batch
	staged := &Store{db: s.db, tx: tx, statements: &batch.Statements}
	value, err := staged.Meta("state_revision")
	if err != nil {
		return Batch{}, err
	}
	if value != "" {
		batch.Revision, err = strconv.ParseUint(value, 10, 64)
		if err != nil {
			return Batch{}, err
		}
	}
	if err := fn(staged); err != nil {
		return Batch{}, err
	}
	if err := tx.Rollback(); err != nil {
		return Batch{}, err
	}
	return batch, nil
}

func (s *Store) ApplyBatch(index uint64, batch Batch) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	target := &Store{db: s.db, tx: tx}
	last, err := target.Meta("fsm_index")
	if err != nil {
		return false, err
	}
	lastIndex, err := parseIndex(last)
	if err != nil {
		return false, err
	}
	if index <= lastIndex {
		return false, nil
	}
	value, err := target.Meta("state_revision")
	if err != nil {
		return false, err
	}
	revision, err := parseIndex(value)
	if err != nil {
		return false, err
	}
	if err := target.SetMeta("fsm_index", strconv.FormatUint(index, 10)); err != nil {
		return false, err
	}
	if revision != batch.Revision {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, ErrStaleBatch
	}
	if revision == ^uint64(0) {
		return false, fmt.Errorf("state revision exhausted")
	}
	for _, statement := range batch.Statements {
		if err := executeStatement(target, statement); err != nil {
			return false, err
		}
	}
	if err := target.SetMeta("state_revision", strconv.FormatUint(revision+1, 10)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) ExportState() ([]Statement, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var statements []Statement
	for _, table := range []struct {
		name, columns, order string
	}{
		{"apps", "name,body,generation", "name"},
		{"app_history", "name,generation,body", "name,generation"},
		{"nodes", "id,body,last_seen,status", "id"},
		{"assignments", "id,app,node_id,body,status", "id"},
		{"actions", "id,at,body", "id"},
		{"routes", "name,body", "name"},
		{"configs", "name,kind,body", "name,kind"},
		{"meta", "key,value", "key"},
		{"snapshots", "idx,body,sig", "idx"},
	} {
		where := ""
		if table.name == "meta" {
			where = " WHERE key NOT IN ('controller_id','controller_raft')"
		}
		statements = append(statements, Statement{SQL: "DELETE FROM " + table.name + where, Args: json.RawMessage("[]")})
		rows, err := tx.Query("SELECT " + table.columns + " FROM " + table.name + where + " ORDER BY " + table.order)
		if err != nil {
			return nil, err
		}
		count := len(strings.Split(table.columns, ","))
		for rows.Next() {
			values := make([]any, count)
			dest := make([]any, count)
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return nil, err
			}
			args, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				return nil, err
			}
			statement := Statement{SQL: "INSERT INTO " + table.name + "(" + table.columns + ") VALUES(" + strings.TrimSuffix(strings.Repeat("?,", count), ",") + ")", Args: args}
			statements = append(statements, statement)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return statements, nil
}

func (s *Store) RestoreState(statements []Statement) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	target := &Store{db: s.db, tx: tx}
	for _, statement := range statements {
		if err := executeStatement(target, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func executeStatement(s *Store, statement Statement) error {
	decoder := json.NewDecoder(bytes.NewReader(statement.Args))
	decoder.UseNumber()
	var args []any
	if err := decoder.Decode(&args); err != nil {
		return err
	}
	for i, value := range args {
		if n, ok := value.(json.Number); ok {
			integer, err := n.Int64()
			if err != nil {
				return err
			}
			args[i] = integer
		}
	}
	_, err := s.exec(statement.SQL, args...)
	return err
}

func (s *Store) exec(query string, args ...any) (sql.Result, error) {
	if s.tx == nil {
		return s.db.Exec(query, args...)
	}
	var raw []byte
	var err error
	if s.statements != nil {
		raw, err = json.Marshal(args)
		if err != nil {
			return nil, err
		}
	}
	result, err := s.tx.Exec(query, args...)
	if err == nil && s.statements != nil {
		*s.statements = append(*s.statements, Statement{SQL: query, Args: raw})
	}
	return result, err
}

func (s *Store) query(query string, args ...any) (*sql.Rows, error) {
	if s.tx != nil {
		return s.tx.Query(query, args...)
	}
	return s.db.Query(query, args...)
}

func (s *Store) queryRow(query string, args ...any) *sql.Row {
	if s.tx != nil {
		return s.tx.QueryRow(query, args...)
	}
	return s.db.QueryRow(query, args...)
}

func parseIndex(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}
