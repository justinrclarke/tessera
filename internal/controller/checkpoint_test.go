package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
)

func checkpointFixture(t *testing.T) (*Server, *client.Client, []api.Assignment, time.Time) {
	t.Helper()
	s, c := standaloneClient(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := c.Register(ctx, client.RegisterRequest{ID: "old", Perf: api.Perf{CPU: 1}, Capacity: api.Resources{CPU: 4000}}); err != nil {
		t.Fatal(err)
	}
	app := api.App{Kind: api.KindJob, Name: "train", Image: "trainer", Replicas: 2, Gang: true, Resources: api.Resources{CPU: 1000}, Checkpoint: &api.Checkpoint{Directory: t.TempDir(), Timeout: "10s"}}
	if err := s.Store.PutApp(app); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(now); err != nil {
		t.Fatal(err)
	}
	asgs, _ := s.Store.ListAssignments()
	for _, a := range asgs {
		if err := c.Report(ctx, a.ID, client.StatusReport{Status: api.StatusRunning}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Register(ctx, client.RegisterRequest{ID: "fast", Perf: api.Perf{CPU: 2}, Capacity: api.Resources{CPU: 4000}}); err != nil {
		t.Fatal(err)
	}
	return s, c, asgs, now
}

func TestCheckpointGangWaitsForEveryMarkerAndStopAcknowledgement(t *testing.T) {
	s, c, old, now := checkpointFixture(t)
	ctx := context.Background()
	state := func() checkpointMove {
		t.Helper()
		raw, _ := s.Store.Meta("checkpoint:train")
		var m checkpointMove
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := state()
	if m.Phase != "saving" || len(m.New) != 2 {
		t.Fatalf("move=%+v", m)
	}
	if err := c.Report(ctx, m.New[0], client.StatusReport{Status: api.StatusRunning}); err == nil {
		t.Fatal("reserved worker ran before checkpoint handshake")
	}
	if err := c.Report(ctx, old[0].ID, client.StatusReport{Status: api.StatusRunning, CheckpointDone: m.ID}); err != nil {
		t.Fatal(err)
	}
	if state().Phase != "saving" {
		t.Fatal("one checkpoint stopped the gang")
	}
	if err := c.Report(ctx, old[1].ID, client.StatusReport{Status: api.StatusRunning, CheckpointDone: "stale-request"}); err != nil {
		t.Fatal(err)
	}
	if state().Phase != "saving" {
		t.Fatal("stale checkpoint stopped the gang")
	}
	if err := c.Report(ctx, old[1].ID, client.StatusReport{Status: api.StatusRunning, CheckpointDone: m.ID}); err != nil {
		t.Fatal(err)
	}
	if state().Phase != "stopping" {
		t.Fatalf("phase=%s", state().Phase)
	}
	if err := c.Report(ctx, old[0].ID, client.StatusReport{Status: api.StatusStopped}); err != nil {
		t.Fatal(err)
	}
	for _, id := range m.New {
		a, _ := s.Store.GetAssignment(id)
		if a.Status != api.StatusReserved {
			t.Fatal("replacement started while another old worker could write")
		}
	}
	if err := c.Report(ctx, old[1].ID, client.StatusReport{Status: api.StatusStopped}); err != nil {
		t.Fatal(err)
	}
	for _, id := range m.New {
		a, _ := s.Store.GetAssignment(id)
		if a.Status != api.StatusPending || a.NodeID != "fast" || a.Env["TESSERA_CHECKPOINT_GENERATION"] != m.ID || a.Env["TESSERA_CHECKPOINT_URI"] != m.Request.URI {
			t.Fatalf("replacement=%+v", a)
		}
		if err := c.Report(ctx, id, client.StatusReport{Status: api.StatusRunning}); err != nil {
			t.Fatal(err)
		}
	}
	if state().Phase != "done" {
		t.Fatal("completed move did not settle")
	}
	if err := s.Reconcile(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	asgs, _ := s.Store.ListAssignments()
	if len(asgs) != 4 {
		t.Fatalf("move duplicated workers: %+v", asgs)
	}
}

func TestCheckpointTimeoutKeepsOriginalGangRunning(t *testing.T) {
	s, _, old, now := checkpointFixture(t)
	for _, id := range []string{"old", "fast"} {
		n, _ := s.Store.GetNode(id)
		n.LastSeen = now.Add(11 * time.Second)
		if err := s.Store.PutNode(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Reconcile(now.Add(11 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, a := range old {
		current, _ := s.Store.GetAssignment(a.ID)
		if current.Status != api.StatusRunning || current.Checkpoint != nil {
			t.Fatalf("timeout changed old worker: %+v", current)
		}
	}
	asgs, _ := s.Store.ListAssignments()
	for _, a := range asgs {
		if a.NodeID == "fast" && api.Active(a.Status) {
			t.Fatal("timeout left a replacement active")
		}
	}
}
