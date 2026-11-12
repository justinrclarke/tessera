package stackbuild

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tessera/internal/api"
)

func TestBuildProvenanceDetectsSourceChangesAndRejectsConcurrentMutation(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "program")
	if err := os.WriteFile(source, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	body := []byte("kind: App\nname: web\nbuild:\n  base: alpine:3.21\n  repository: example/web\n  command: [cat, program]\n")
	mutate := false
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "build" && mutate {
			if err := os.WriteFile(source, []byte("changed during build"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if args[0] == "image" && args[1] == "inspect" {
			return []byte("sha256:" + strings.Repeat("a", 64)), nil
		}
		return nil, nil
	}
	prepared, _, err := Prepare(context.Background(), filepath.Join(dir, "app.yaml"), body, run)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := api.DecodeAll(prepared)
	if err != nil {
		t.Fatal(err)
	}
	record := objects[0].App.Artifact
	if record == nil || record.Image != objects[0].App.Image || record.ContextDigest == "" || record.Base != "alpine:3.21" || len(record.Command) != 2 {
		t.Fatalf("missing build provenance: %+v", record)
	}
	if err := os.WriteFile(source, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	second, _, err := Prepare(context.Background(), filepath.Join(dir, "app.yaml"), body, run)
	if err != nil {
		t.Fatal(err)
	}
	objects, err = api.DecodeAll(second)
	if err != nil || objects[0].App.Artifact.ContextDigest == record.ContextDigest {
		t.Fatalf("source mutation did not change provenance: %v", err)
	}
	mutate = true
	if _, _, err := Prepare(context.Background(), filepath.Join(dir, "app.yaml"), body, run); err == nil || !strings.Contains(err.Error(), "context changed") {
		t.Fatalf("concurrent source mutation was accepted: %v", err)
	}
}
