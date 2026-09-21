package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type File struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func Dir() string {
	if v := os.Getenv("TESSERA_DATA"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".tessera"
	}
	return filepath.Join(home, ".tessera")
}

func Load(dir string) (File, error) {
	b, err := os.ReadFile(filepath.Join(dir, "client.json"))
	if err != nil {
		return File{}, err
	}
	var f File
	err = json.Unmarshal(b, &f)
	return f, err
}

func Save(dir string, f File) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "client.json"), b, 0o600)
}
