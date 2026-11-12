package runtime

import (
	"fmt"
	"os"
	"path/filepath"

	"tessera/internal/api"
)

func validateStorage(storage *api.Storage) error {
	if storage == nil {
		return nil
	}
	if err := storage.Validate(); err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(storage.Source)
	if err != nil {
		return fmt.Errorf("storage source must already exist: %w", err)
	}
	if canonical != storage.Source {
		return fmt.Errorf("storage source must use its canonical path %s", canonical)
	}
	info, err := os.Stat(storage.Source)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("storage source must be an existing directory")
	}
	return nil
}
