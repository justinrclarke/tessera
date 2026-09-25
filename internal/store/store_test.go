package store

import (
	"path/filepath"
	"testing"
	"time"

	"tessera/internal/api"
)

func TestAppGenerationAndRollback(t *testing.T) {
	s := open(t)
	app := api.App{Kind: api.KindApp, Name: "web", Image: "nginx:1", Replicas: 1}
	if err := s.PutApp(app); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetApp("web")
	if err != nil || got.Generation != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if err := s.MarkHealthy("web", 1); err != nil {
		t.Fatal(err)
	}
	app.Image = "nginx:2"
	if err := s.PutApp(app); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetApp("web")
	if got.Generation != 2 || got.HealthyGeneration != 1 || got.Image != "nginx:2" {
		t.Fatalf("%+v", got)
	}
	rolled, err := s.Rollback("web")
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Image != "nginx:1" || rolled.Generation != 1 {
		t.Fatalf("%+v", rolled)
	}
}

func TestPendingNodeActionBeyondRecentActions(t *testing.T) {
	s := open(t)
	base := time.Now()
	if err := s.AddAction(api.Action{ID: "confirmed", At: base, Kind: "wipe", Target: "n1", Result: "confirmed"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if err := s.AddAction(api.Action{ID: api.NewID(), At: base.Add(time.Duration(i+1) * time.Second), Kind: "move", Target: "web", Result: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.PendingNodeAction("n1")
	if err != nil || a.ID != "confirmed" {
		t.Fatalf("action %+v: %v", a, err)
	}
}

func TestBackup(t *testing.T) {
	s := open(t)
	if err := s.SetMeta("k", "v"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bak.db")
	if err := s.Backup(path); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	v, err := s2.Meta("k")
	if err != nil || v != "v" {
		t.Fatalf("%q %v", v, err)
	}
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
