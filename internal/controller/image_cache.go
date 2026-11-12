package controller

import (
	"fmt"
	"os"
	"time"

	"tessera/internal/api"
	"tessera/internal/images"
)

func imageLimits() (images.Limits, error) {
	limits := images.Defaults()
	for key, target := range map[string]*int64{
		"TESSERA_IMAGE_CACHE_BYTES":    &limits.MaxBytes,
		"TESSERA_IMAGE_CACHE_MIN_FREE": &limits.MinFree,
	} {
		if value, ok := os.LookupEnv(key); ok {
			n, err := api.ParseMemory(value)
			if err != nil || n < 0 || key == "TESSERA_IMAGE_CACHE_BYTES" && n == 0 {
				return limits, fmt.Errorf("%s must be a valid byte quantity", key)
			}
			*target = n
		}
	}
	return limits, nil
}

func (s *Server) pruneImages() error {
	if s.DataDir == "" {
		return nil
	}
	s.mu.Lock()
	keep, err := s.Store.ImageReferences()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return images.Prune(s.DataDir, keep, s.now().Add(-24*time.Hour))
}
