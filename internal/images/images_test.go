package images

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if Has(dir, "nginx:alpine") {
		t.Fatal("present")
	}
	if err := Save(dir, "nginx:alpine", strings.NewReader("blob")); err != nil {
		t.Fatal(err)
	}
	if !Has(dir, "nginx:alpine") {
		t.Fatal("missing")
	}
	f, err := Open(dir, "nginx:alpine")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "blob" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestCapacityPreservesPreviousImageAndRemovesPartial(t *testing.T) {
	dir := t.TempDir()
	limits := Limits{MaxBytes: 10, free: func(string) int64 { return 100 }}
	if err := SaveLimited(dir, "web", strings.NewReader("good"), limits); err != nil {
		t.Fatal(err)
	}
	if err := SaveLimited(dir, "web", strings.NewReader("too large"), limits); !errors.Is(err, ErrCapacity) {
		t.Fatalf("oversized upload: %v", err)
	}
	body, err := os.ReadFile(File(dir, "web"))
	if err != nil || string(body) != "good" {
		t.Fatalf("previous image changed: %q %v", body, err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "images"))
	if len(entries) != 1 {
		t.Fatalf("left partial files: %v", entries)
	}
}

func TestDiskReserveAndConcurrentUploads(t *testing.T) {
	dir := t.TempDir()
	limits := Limits{MaxBytes: 10, MinFree: 20, free: func(string) int64 { return 22 }}
	if err := SaveLimited(dir, "web", strings.NewReader("123"), limits); !errors.Is(err, ErrCapacity) {
		t.Fatalf("disk reserve: %v", err)
	}
	limits.MinFree = 0
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, ref := range []string{"one", "two"} {
		wg.Go(func() { results <- SaveLimited(dir, ref, strings.NewReader("123456"), limits) })
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrCapacity) {
			t.Fatal(err)
		}
	}
	if used, err := Usage(dir); err != nil || used > 10 || success != 1 {
		t.Fatalf("usage=%d success=%d error=%v", used, success, err)
	}
}

func TestPruneRetainsReferencesAndRecentlyUsedImages(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	for _, ref := range []string{"current", "rollback", "recent", "unused"} {
		if err := Save(dir, ref, strings.NewReader(ref)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(File(dir, ref), old, old); err != nil {
			t.Fatal(err)
		}
	}
	f, err := Open(dir, "recent")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := Prune(dir, map[string]bool{"current": true, "rollback": true}, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"current", "rollback", "recent"} {
		if !Has(dir, ref) {
			t.Fatalf("lost %s", ref)
		}
	}
	if Has(dir, "unused") {
		t.Fatal("unused image retained")
	}
}
