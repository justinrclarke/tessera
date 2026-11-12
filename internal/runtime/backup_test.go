package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/volume"
)

type failingBackupWriter struct{}

func (failingBackupWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("disk full") }

func TestDockerStorageBackupAlwaysResumesAndRestores(t *testing.T) {
	if os.Getenv("TESSERA_DOCKER_SMOKE") == "" {
		t.Skip("set TESSERA_DOCKER_SMOKE=1 for storage backup acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "saved"), []byte("durable data"), 0600); err != nil {
		t.Fatal(err)
	}
	storage := &api.Storage{Node: "backup-node", Source: directory, Target: "/data"}
	id := api.NewID()
	d := NewDocker(DockerSock())
	c, err := d.Start(ctx, Spec{Name: "tessera_" + id, Image: "busybox:latest", Owner: storage.Node, Storage: storage, Command: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop(context.Background(), c.ID)
	assignment := api.Assignment{ID: id, NodeID: storage.Node, RuntimeID: c.ID, Storage: storage}
	if err := d.BackupStorage(ctx, assignment, failingBackupWriter{}); err == nil {
		t.Fatal("backup ignored write failure")
	}
	body, err := d.call(ctx, "GET", "/v1.41/containers/"+c.ID+"/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		State struct{ Running, Paused bool }
	}
	if err := json.Unmarshal(body, &state); err != nil || !state.State.Running || state.State.Paused {
		t.Fatalf("container not resumed: %s %v", body, err)
	}
	var archive bytes.Buffer
	if err := d.BackupStorage(ctx, assignment, &archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restore")
	if err := volume.Restore(&archive, destination, 1<<20); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "saved"))
	if err != nil || string(data) != "durable data" {
		t.Fatalf("%q %v", data, err)
	}
}
