package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
)

func TestCheckpointPartitionAndTargetRecoveryDoNotDuplicateWorkers(t *testing.T) {
	s, c, old, now := checkpointFixture(t)
	clock := now
	s.Now = func() time.Time { return clock }
	ctx := context.Background()
	read := func() checkpointMove {
		t.Helper()
		raw, _ := s.Store.Meta("checkpoint:train")
		var move checkpointMove
		if err := json.Unmarshal([]byte(raw), &move); err != nil {
			t.Fatal(err)
		}
		return move
	}
	move := read()
	for _, a := range old {
		if err := c.Report(ctx, a.ID, client.StatusReport{Status: api.StatusRunning, CheckpointDone: move.ID, CheckpointProgress: 100}); err != nil {
			t.Fatal(err)
		}
	}
	if read().Phase != "stopping" {
		t.Fatal("did not request stop")
	}
	if err := c.Report(ctx, old[0].ID, client.StatusReport{Status: api.StatusStopped}); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(time.Minute)
	if err := s.Reconcile(clock); err != nil {
		t.Fatal(err)
	}
	for _, id := range move.New {
		a, _ := s.Store.GetAssignment(id)
		if a.Status != api.StatusReserved {
			t.Fatalf("unacknowledged old worker was replaced: %+v", a)
		}
	}
	if err := c.Report(ctx, old[1].ID, client.StatusReport{Status: api.StatusStopped}); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(2 * time.Minute)
	if err := s.Reconcile(clock); err != nil {
		t.Fatal(err)
	}
	asgs, _ := s.Store.ListAssignments()
	if len(asgs) != 4 || read().Phase != "starting" {
		t.Fatalf("lost reserved targets: %+v", asgs)
	}
	if _, err := c.Register(ctx, client.RegisterRequest{ID: "fast", Perf: api.Perf{CPU: 2}, Capacity: api.Resources{CPU: 4000}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range move.New {
		a, _ := s.Store.GetAssignment(id)
		if a.NodeID != "fast" || a.Status != api.StatusPending || a.Env["TESSERA_CHECKPOINT_PROGRESS"] != "100" {
			t.Fatalf("%+v", a)
		}
		if err := c.Report(ctx, id, client.StatusReport{Status: api.StatusRunning}); err != nil {
			t.Fatal(err)
		}
	}
	if read().Phase != "done" {
		t.Fatal("recovered target did not complete move")
	}
}
