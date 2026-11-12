package api

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Storage struct {
	Node   string `json:"node" yaml:"node"`
	Source string `json:"source" yaml:"source"`
	Target string `json:"target" yaml:"target"`
}

func (s Storage) Validate() error {
	if s.Node == "" {
		return fmt.Errorf("storage requires a fixed node ID")
	}
	for _, value := range []string{s.Source, s.Target} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" || strings.ContainsAny(value, "\x00\r\n,:") {
			return fmt.Errorf("storage requires clean absolute source and target directories other than /")
		}
	}
	return nil
}

func StorageOverlaps(a, b *Storage) bool {
	if a == nil || b == nil || a.Node != b.Node {
		return false
	}
	return a.Source == b.Source || strings.HasPrefix(a.Source, b.Source+"/") || strings.HasPrefix(b.Source, a.Source+"/")
}
