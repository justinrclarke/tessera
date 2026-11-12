package drill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tessera/internal/agent"
	"tessera/internal/api"
	"tessera/internal/fileutil"
	"tessera/internal/runtime"
)

type checkpointWorker struct {
	*runtime.Fake
	resumed map[string]int64
}

func (w *checkpointWorker) Start(ctx context.Context, spec runtime.Spec) (runtime.Container, error) {
	if uri := spec.Env["TESSERA_CHECKPOINT_URI"]; uri != "" {
		path := filepath.Join(strings.TrimPrefix(uri, "file://"), spec.Env["TESSERA_CHECKPOINT_SOURCE_ASSIGNMENT"], "progress")
		body, err := os.ReadFile(path)
		if err != nil {
			return runtime.Container{}, err
		}
		progress, err := strconv.ParseInt(string(body), 10, 64)
		if err != nil || spec.Env["TESSERA_CHECKPOINT_PROGRESS"] != string(body) {
			return runtime.Container{}, fmt.Errorf("resume metadata differs from saved progress")
		}
		w.resumed[spec.Env["TESSERA_WORKER_RANK"]] = progress + 1
	}
	return w.Fake.Start(ctx, spec)
}

func checkpointGang() error {
	ctx := context.Background()
	now := time.Now()
	s, cl, cleanup := boot(now)
	defer cleanup()
	dir := tDir()
	defer os.RemoveAll(dir)
	shared := filepath.Join(dir, "shared")
	if err := os.MkdirAll(shared, 0700); err != nil {
		return err
	}
	body := fmt.Sprintf("kind: Job\nname: training\nimage: trainer\nreplicas: 2\ngang: true\nsensitive_to: cpu\nnode_labels: {pool: training}\nresources: {cpu: 1m}\ncheckpoint: {directory: %s, timeout: 1m}\n", shared)
	if err := cl.Apply(ctx, []byte(body)); err != nil {
		return err
	}
	oldRuntime := runtime.NewFake()
	old := &agent.Agent{ID: "slow", DataDir: filepath.Join(dir, "old"), Token: "drill-token", Client: cl, Runtime: oldRuntime, Labels: map[string]string{"pool": "training"}}
	if err := old.Tick(ctx); err != nil {
		return err
	}
	worker := &checkpointWorker{Fake: runtime.NewFake(), resumed: map[string]int64{}}
	next := &agent.Agent{ID: "fast", DataDir: filepath.Join(dir, "new"), Token: "drill-token", Client: cl, Runtime: worker, Labels: map[string]string{"pool": "waiting"}}
	if err := next.Tick(ctx); err != nil {
		return err
	}
	for id, score := range map[string]float64{"slow": 1, "fast": 1e12} {
		node, err := s.Store.GetNode(id)
		if err != nil {
			return err
		}
		node.Perf.CPU, node.Labels["pool"] = score, "training"
		if err := s.Store.PutNode(node); err != nil {
			return err
		}
	}
	next.Labels["pool"] = "training"
	if err := s.Reconcile(now); err != nil {
		return err
	}
	if err := old.Tick(ctx); err != nil {
		return err
	}
	assignments, err := s.Store.ListAssignments()
	if err != nil {
		return err
	}
	committed := map[string]int64{}
	for _, a := range assignments {
		if a.NodeID != "slow" {
			if a.Status != api.StatusReserved {
				return fmt.Errorf("replacement ran before checkpoint: %s", a.Status)
			}
			continue
		}
		requestPath := filepath.Join(shared, "requests", a.ID+".json")
		raw, err := os.ReadFile(requestPath)
		if err != nil {
			return err
		}
		var request api.CheckpointRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return err
		}
		progress := int64(40 + len(committed))
		path := filepath.Join(strings.TrimPrefix(request.URI, "file://"), a.ID, "progress")
		if err := fileutil.Write(path, []byte(strconv.FormatInt(progress, 10)), 0600); err != nil {
			return err
		}
		marker, _ := json.Marshal(api.CheckpointMarker{RequestID: request.ID, AssignmentID: a.ID, URI: request.URI, Progress: progress})
		if err := fileutil.Write(filepath.Join(shared, "committed", request.ID, a.ID+".json"), marker, 0600); err != nil {
			return err
		}
		committed[a.Env["TESSERA_WORKER_RANK"]] = progress
	}
	for range 4 {
		if err := old.Tick(ctx); err != nil {
			return err
		}
		if err := next.Tick(ctx); err != nil {
			return err
		}
	}
	items, err := oldRuntime.List(ctx)
	if err != nil || len(items) != 0 {
		return fmt.Errorf("old workers survived cutover: %+v: %v", items, err)
	}
	if len(committed) != 2 || len(worker.resumed) != 2 {
		return fmt.Errorf("incomplete gang: saved=%v resumed=%v", committed, worker.resumed)
	}
	for rank, progress := range committed {
		if worker.resumed[rank] <= progress {
			return fmt.Errorf("worker %s did not advance beyond checkpoint", rank)
		}
	}
	return nil
}
