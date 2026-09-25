package store

import (
	"os"
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

func TestRestoreBackupAdvancesEpochAndChangesIdentity(t *testing.T) {
	s := open(t)
	for key, value := range map[string]string{"token": "shared-token", "epoch": "7", "controller_id": "old-leader"} {
		if err := s.SetMeta(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutApp(api.App{Kind: api.KindApp, Name: "web", Image: "nginx:1", Replicas: 1}); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "new", "tessera.db")
	if err := RestoreBackup(backup, restored, 12); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBackup(backup, restored, 0); err == nil {
		t.Fatal("restore overwrote an existing target")
	}
	next, err := Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	for key, want := range map[string]string{"token": "shared-token", "epoch": "12", "controller_id": ""} {
		got, err := next.Meta(key)
		if err != nil || got != want {
			t.Fatalf("%s=%q, want %q: %v", key, got, want, err)
		}
	}
	if app, err := next.GetApp("web"); err != nil || app.Image != "nginx:1" {
		t.Fatalf("app %+v: %v", app, err)
	}
	info, err := os.Stat(restored)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored mode: %v %v", info, err)
	}
}

func TestRestoreBackupRejectsCorruptSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "bad.db")
	if err := os.WriteFile(source, []byte("not SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "tessera.db")
	if err := RestoreBackup(source, target, 0); err == nil {
		t.Fatal("accepted corrupt backup")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target exists after failed restore: %v", err)
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
