package config

import (
	"encoding/json"
	"os"
	"path/filepath"

	"tessera/internal/fileutil"
)

type File struct {
	Controllers []string `json:"controllers,omitempty"`
	URL         string   `json:"url"`
	Token       string   `json:"token"`
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
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.Write(filepath.Join(dir, "client.json"), b, 0o600)
}
