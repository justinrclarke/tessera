package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/runtime"
)

type failedStop struct{ runtime.Runtime }

func (failedStop) Stop(context.Context, string) error { return errors.New("stop failed") }

func TestPerfRecordedOnceThenHourly(t *testing.T) {
	dir := t.TempDir()
	cur := time.Now()
	ag := &Agent{DataDir: dir, Now: func() time.Time { return cur }}
	first := ag.perf()
	if ag.perfAt != cur {
		t.Fatalf("perf at %s", ag.perfAt)
	}
	cur = cur.Add(30 * time.Minute)
	again := ag.perf()
	if again != first || ag.perfAt.Equal(cur) {
		t.Fatal("rebenched inside the hour")
	}
	saved, err := ag.readCache()
	if err != nil {
		t.Fatal(err)
	}
	cur = saved.PerfAt.Add(30 * time.Minute)
	ag2 := &Agent{DataDir: dir, Now: func() time.Time { return cur }}
	loaded := ag2.perf()
	if loaded != saved.Perf {
		t.Fatal("ignored recorded benchmark")
	}
	if !ag2.perfAt.Equal(saved.PerfAt) {
		t.Fatal("rebenched on load")
	}
	cur = saved.PerfAt.Add(time.Hour)
	_ = ag2.perf()
	if !ag2.perfAt.Equal(cur) {
		t.Fatal("did not refresh")
	}
}

func TestGPUInventoryRefreshesAndReports(t *testing.T) {
	cur := time.Now()
	calls := 0
	ag := &Agent{DataDir: t.TempDir(), Now: func() time.Time { return cur }, GPUProbe: func(context.Context) ([]api.GPU, error) {
		calls++
		return []api.GPU{{UUID: "gpu-one", Model: "NVIDIA H100", MemoryFree: 20 << 30}}, nil
	}}
	first := ag.reportHost()
	if first.GPUs != 1 || len(first.GPUInventory) != 1 || calls != 1 {
		t.Fatalf("first report %+v, calls %d", first, calls)
	}
	cur = cur.Add(10 * time.Second)
	_ = ag.reportHost()
	if calls != 1 {
		t.Fatalf("probed %d times inside refresh window", calls)
	}
	cur = cur.Add(30 * time.Second)
	_ = ag.reportHost()
	if calls != 2 {
		t.Fatalf("did not refresh: %d calls", calls)
	}
}

func TestRestartRemovesExitedContainer(t *testing.T) {
	ctx := context.Background()
	fake := runtime.NewFake()
	ag := &Agent{ID: "n1", DataDir: t.TempDir(), Runtime: fake}
	desired := []api.Assignment{{ID: "a", App: "web", Image: "nginx", Status: api.StatusRunning}}
	if err := ag.ConvergeForTest(ctx, desired, true); err != nil {
		t.Fatal(err)
	}
	fake.Kill("tessera_a", "crash", false)
	if err := ag.ConvergeForTest(ctx, desired, true); err != nil {
		t.Fatal(err)
	}
	items, err := fake.List(ctx)
	if err != nil || len(items) != 1 || !items[0].Running {
		t.Fatalf("restart left %+v: %v", items, err)
	}
}

func TestWipeKeepsStateWhenStopFails(t *testing.T) {
	ctx := context.Background()
	base := runtime.NewFake()
	dir := t.TempDir()
	ag := &Agent{ID: "n1", DataDir: dir, Runtime: base}
	desired := []api.Assignment{{ID: "a", App: "web", Image: "nginx", Status: api.StatusRunning}}
	if err := ag.ConvergeForTest(ctx, desired, true); err != nil {
		t.Fatal(err)
	}
	ag.Runtime = failedStop{base}
	if err := ag.clearLocal(ctx, false); err == nil {
		t.Fatal("wipe reported success despite failed stop")
	}
	if _, err := os.Stat(filepath.Join(dir, "cache.json")); err != nil {
		t.Fatalf("removed cache after failed stop: %v", err)
	}
}
