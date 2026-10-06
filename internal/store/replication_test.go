package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"tessera/internal/api"

	"github.com/hashicorp/raft"
)

func TestCaptureCommitsAtomicallyAndPreservesIntegers(t *testing.T) {
	s := open(t)
	batch, err := s.Capture(func(staged *Store) error {
		if err := staged.PutApp(api.App{Name: "web", Image: "nginx", Generation: 9007199254740993}); err != nil {
			return err
		}
		app, err := staged.GetApp("web")
		if err != nil || app.Generation != 9007199254740993 {
			t.Fatalf("staged app %+v: %v", app, err)
		}
		return staged.SetMeta("test", "committed")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetApp("web"); err == nil {
		t.Fatal("prepared change was visible before consensus")
	}
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Batch
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.ApplyBatch(7, decoded); err != nil || !changed {
		t.Fatalf("commit %v: %v", changed, err)
	}
	app, err := s.GetApp("web")
	if err != nil || app.Generation != 9007199254740993 {
		t.Fatalf("committed app %+v: %v", app, err)
	}
	if changed, err := s.ApplyBatch(7, decoded); err != nil || changed {
		t.Fatalf("replayed committed command: %v, %v", changed, err)
	}
	bad, err := s.Capture(func(staged *Store) error { return staged.PutApp(api.App{Name: "failed", Image: "nginx"}) })
	if err != nil {
		t.Fatal(err)
	}
	bad.Statements = append(bad.Statements, Statement{SQL: "INSERT INTO missing_table VALUES(?)", Args: json.RawMessage(`["fail"]`)})
	if _, err := s.ApplyBatch(8, bad); err == nil {
		t.Fatal("accepted a failing command")
	}
	if _, err := s.GetApp("failed"); err == nil {
		t.Fatal("part of a failed command became visible")
	}
	index, err := s.Meta("fsm_index")
	if err != nil || index != "7" {
		t.Fatalf("failed command advanced the durable index: %s, %v", index, err)
	}
}

func TestStalePreparedCommandCannotOverwriteCommittedState(t *testing.T) {
	s := open(t)
	first, err := s.Capture(func(staged *Store) error { return staged.PutApp(api.App{Name: "web", Image: "v1"}) })
	if err != nil {
		t.Fatal(err)
	}
	stale, err := s.Capture(func(staged *Store) error { return staged.PutApp(api.App{Name: "web", Image: "v2"}) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyBatch(1, first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyBatch(2, stale); !errors.Is(err, ErrStaleBatch) {
		t.Fatalf("stale command accepted: %v", err)
	}
	app, err := s.GetApp("web")
	if err != nil || app.Image != "v1" {
		t.Fatalf("stale command changed state: %+v, %v", app, err)
	}
}

func TestStateSnapshotPreservesHistoryAndSignedBytes(t *testing.T) {
	s := open(t)
	if err := s.PutApp(api.App{Name: "web", Image: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkHealthy("web", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.PutApp(api.App{Name: "web", Image: "v2"}); err != nil {
		t.Fatal(err)
	}
	raw := "{ \"index\":1, \"epoch\":2, \"apps\":[] }"
	if err := s.AppendSnapshotRaw(1, raw, "raw-body-signature"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta("controller_id", "first"); err != nil {
		t.Fatal(err)
	}
	state, err := s.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	target := open(t)
	if err := target.SetMeta("controller_id", "second"); err != nil {
		t.Fatal(err)
	}
	if err := target.RestoreState(state); err != nil {
		t.Fatal(err)
	}
	app, err := target.Rollback("web")
	if err != nil || app.Image != "v1" || app.HealthyGeneration != 1 {
		t.Fatalf("snapshot lost rollback history: %+v, %v", app, err)
	}
	body, signature, err := target.RawSnapshot()
	if err != nil || body != raw || signature != "raw-body-signature" {
		t.Fatalf("snapshot changed signed bytes: %q, %q, %v", body, signature, err)
	}
	id, err := target.Meta("controller_id")
	if err != nil || id != "second" {
		t.Fatalf("snapshot overwrote local controller identity: %s, %v", id, err)
	}
}

func TestRaftStorageSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raft.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.RaftStore()
	if err != nil {
		t.Fatal(err)
	}
	logs := []*raft.Log{{Index: 3, Term: 2, Type: raft.LogCommand, Data: []byte("command"), Extensions: []byte("extension"), AppendedAt: time.Now().UTC()}, {Index: 4, Term: 2, Type: raft.LogNoop}}
	if err := r.StoreLogs(logs); err != nil {
		t.Fatal(err)
	}
	if err := r.SetUint64([]byte("term"), ^uint64(0)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err = s.RaftStore()
	if err != nil {
		t.Fatal(err)
	}
	var log raft.Log
	if err := r.GetLog(3, &log); err != nil || !reflect.DeepEqual(log, *logs[0]) {
		t.Fatalf("log changed on reopen: %+v, %v", log, err)
	}
	if term, err := r.GetUint64([]byte("term")); err != nil || term != ^uint64(0) {
		t.Fatalf("term lost: %d, %v", term, err)
	}
	if err := r.DeleteRange(3, 3); err != nil {
		t.Fatal(err)
	}
	if err := r.GetLog(3, &log); !errors.Is(err, raft.ErrLogNotFound) {
		t.Fatalf("deleted log remains: %v", err)
	}
	first, err := r.FirstIndex()
	if err != nil || first != 4 {
		t.Fatalf("first index %d: %v", first, err)
	}
	last, err := r.LastIndex()
	if err != nil || last != 4 {
		t.Fatalf("last index %d: %v", last, err)
	}
}
