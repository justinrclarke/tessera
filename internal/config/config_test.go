package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveReplacesPermissiveCredentialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, File{URL: "http://controller", Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved credentials are readable by other users: %v %v", info, err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded.Token != "secret" {
		t.Fatalf("credentials not persisted: %+v %v", loaded, err)
	}
}
