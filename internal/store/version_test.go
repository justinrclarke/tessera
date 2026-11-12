package store

import (
	"path/filepath"
	"strings"
	"testing"

	"tessera/internal/api"
)

func TestSchemaUpgradeAndFutureVersionRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tessera.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutApp(api.App{Name: "preserved", Image: "image", Kind: api.KindApp}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec("PRAGMA user_version=0"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if app, err := s.GetApp("preserved"); err != nil || app.Image != "image" {
		t.Fatalf("%+v %v", app, err)
	}
	var version int
	if err := s.queryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("version %d: %v", version, err)
	}
	if _, err := s.exec("PRAGMA user_version=2147483647"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("opened future schema")
	} else if !strings.Contains(err.Error(), "newer Tessera binary") {
		t.Fatal(err)
	}
}
