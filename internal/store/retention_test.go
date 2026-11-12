package store

import (
	"fmt"
	"testing"
	"time"

	"tessera/internal/api"
)

func TestRetentionPreservesJobProgressPendingActionsAndHealthyHistory(t *testing.T) {
	s := open(t)
	err := s.Transaction(func(st *Store) error {
		if err := st.PutApp(api.App{Name: "job", Kind: api.KindJob, Image: "train", Replicas: 1}); err != nil {
			return err
		}
		for i := range 1100 {
			if err := st.PutAssignment(api.Assignment{ID: fmt.Sprintf("old-%04d", i), App: "old", Status: api.StatusStopped, Updated: time.Unix(int64(i), 0)}); err != nil {
				return err
			}
			if err := st.AddAction(api.Action{ID: fmt.Sprintf("done-%04d", i), Result: "done", At: time.Unix(int64(i), 0)}); err != nil {
				return err
			}
		}
		for _, result := range []string{"proposed", "confirmed"} {
			if err := st.AddAction(api.Action{ID: result, Result: result}); err != nil {
				return err
			}
		}
		if err := st.PutAssignment(api.Assignment{ID: "completed-worker", App: "job", Generation: 1, Status: api.StatusSucceeded}); err != nil {
			return err
		}
		if err := st.PutAssignment(api.Assignment{ID: "failed-worker", App: "job", Generation: 1, Status: api.StatusFailed}); err != nil {
			return err
		}
		for i := 1; i <= 30; i++ {
			if err := st.PutApp(api.App{Name: "web", Kind: api.KindApp, Image: fmt.Sprintf("v%d", i), HealthyGeneration: 1}); err != nil {
				return err
			}
			if i == 1 {
				if err := st.MarkHealthy("web", 1); err != nil {
					return err
				}
			}
			if err := st.AppendSnapshotRaw(uint64(i), fmt.Sprint(i), "signature"); err != nil {
				return err
			}
		}
		return st.PruneHistory()
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"completed-worker", "failed-worker"} {
		if _, err := s.GetAssignment(id); err != nil {
			t.Fatalf("removed desired Job state %s: %v", id, err)
		}
	}
	if a, _ := s.ListAssignments(); len(a) != 1002 {
		t.Fatalf("assignment retention: %d", len(a))
	}
	if a, _ := s.ListActions(2000); len(a) != 1002 {
		t.Fatalf("action retention: %d", len(a))
	}
	var count int
	if err := s.queryRow("SELECT COUNT(*) FROM snapshots").Scan(&count); err != nil || count != 3 {
		t.Fatalf("snapshot retention: %d %v", count, err)
	}
	if _, err := s.History("web", 1); err != nil {
		t.Fatal("healthy rollback release was pruned")
	}
	if _, err := s.History("web", 2); err == nil {
		t.Fatal("old release history was retained without a bound")
	}
	rolled, err := s.Rollback("web")
	if err != nil || rolled.Generation != 1 {
		t.Fatalf("healthy rollback failed: %+v %v", rolled, err)
	}
}
