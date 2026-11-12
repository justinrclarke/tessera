package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/fileutil"
)

func TestCheckpointRequiresAtomicMarkerForThisWorkerAndRequest(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	a := Agent{Now: func() time.Time { return now }}
	r := api.CheckpointRequest{ID: "move", Directory: dir, URI: "file://" + dir + "/generations/move", Deadline: now.Add(time.Minute)}
	asg := api.Assignment{ID: "worker", Status: api.StatusRunning, Checkpoint: &r}
	if a.checkpoint(asg) != "" {
		t.Fatal("missing checkpoint accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "requests", "worker.json")); err != nil {
		t.Fatal(err)
	}
	marker := api.CheckpointMarker{RequestID: "stale", AssignmentID: asg.ID, URI: r.URI, Progress: 100}
	write := func() {
		body, _ := json.Marshal(marker)
		if err := fileutil.Write(filepath.Join(dir, "committed", r.ID, asg.ID+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if a.checkpoint(asg) != "" {
		t.Fatal("stale marker accepted")
	}
	marker.RequestID = r.ID
	marker.AssignmentID = "someone-else"
	write()
	if a.checkpoint(asg) != "" {
		t.Fatal("another worker marker accepted")
	}
	marker.AssignmentID = asg.ID
	write()
	if a.checkpoint(asg) != r.ID {
		t.Fatal("committed marker ignored")
	}
	now = now.Add(2 * time.Minute)
	if a.checkpoint(asg) != "" {
		t.Fatal("expired request accepted")
	}
}
