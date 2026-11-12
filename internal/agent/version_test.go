package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"tessera/internal/runtime"
)

func TestFutureCacheIsRefusedWithoutRewritingOrStarting(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"format_version":999,"node_id":"saved","assignments":[{"id":"writer","image":"image","status":"running"}]}`)
	if err := os.WriteFile(filepath.Join(dir, "cache.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	rtm := runtime.NewFake()
	a := Agent{DataDir: dir, Runtime: rtm}
	a.init()
	if err := a.restore(context.Background()); err == nil {
		t.Fatal("future cache accepted")
	}
	after, err := os.ReadFile(filepath.Join(dir, "cache.json"))
	if err != nil || string(after) != string(body) {
		t.Fatal("future cache was changed")
	}
	items, _ := rtm.List(context.Background())
	if len(items) != 0 {
		t.Fatal("future assignments started")
	}
}
