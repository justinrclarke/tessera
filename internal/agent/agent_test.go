package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/runtime"
	"tessera/internal/survive"
)

func TestNewEpochAcceptsEarlierSnapshotIndexAndUsesSignedBody(t *testing.T) {
	ctx := context.Background()
	actual := api.Snapshot{Index: 2, Epoch: 5, LeaderID: "restored", Apps: []api.App{{Name: "web", Image: "restored"}}}
	body, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(client.SignedSnapshot{Snapshot: api.Snapshot{Index: 100, Epoch: 99}, Body: string(body), Sig: survive.Sign("token", body)})
	}))
	defer srv.Close()
	ag := &Agent{ID: "n1", DataDir: t.TempDir(), Client: client.New(srv.URL, "token"), Token: "token", maxEpoch: 5, snapEpoch: 4, snapIndex: 100}
	if err := ag.fetchSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if ag.snapIndex != 2 || ag.snapEpoch != 5 || ag.maxEpoch != 5 {
		t.Fatalf("accepted unsigned fields or rejected restored snapshot: %+v", ag)
	}
	cache, err := ag.readCache()
	if err != nil || cache.SnapBody != string(body) || !survive.Verify("token", []byte(cache.SnapBody), cache.Sig) {
		t.Fatal("changed signed raw snapshot bytes")
	}
	cache.Snapshot.Epoch = 99
	if err := ag.writeCache(cache); err != nil {
		t.Fatal(err)
	}
	loaded, err := ag.loadSnapshot()
	if err != nil || loaded.Epoch != 5 || loaded.Index != 2 {
		t.Fatalf("loaded unsigned cache fields: %+v %v", loaded, err)
	}
	ag.maxEpoch = 6
	if err := ag.fetchSnapshot(ctx); err == nil {
		t.Fatal("accepted snapshot from an earlier epoch")
	}
}

func TestStalePageAndHeartbeatCannotStopRunningWork(t *testing.T) {
	for _, staleHeartbeat := range []bool{false, true} {
		t.Run(fmt.Sprint(staleHeartbeat), func(t *testing.T) {
			now := time.Now()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var response any
				switch r.URL.Path {
				case "/v1/nodes/register":
					response = client.RegisterResponse{Epoch: 4}
				case "/v1/nodes/n1/heartbeat":
					hb := client.HeartbeatResponse{Epoch: 4, Leading: true, Expires: now.Add(time.Minute)}
					if staleHeartbeat {
						hb.Epoch = 3
						hb.Command = &client.NodeCommand{ID: "old-wipe", Kind: "wipe"}
					}
					response = hb
				case "/v1/nodes/n1/assignments":
					response = client.Page{Epoch: 3}
				default:
					response = struct{}{}
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer srv.Close()
			fake := runtime.NewFake()
			ag := &Agent{ID: "n1", DataDir: t.TempDir(), Runtime: fake, Now: func() time.Time { return now }, GPUProbe: func(context.Context) ([]api.GPU, error) { return nil, nil }}
			epoch := uint64(3)
			if staleHeartbeat {
				epoch = 4
			}
			ag.SetEpoch(epoch)
			if err := ag.ConvergeForTest(context.Background(), []api.Assignment{{ID: "keep", App: "web", Image: "nginx", Epoch: epoch, Status: api.StatusRunning}}, true); err != nil {
				t.Fatal(err)
			}
			ag.Client = client.New(srv.URL, "token")
			if err := ag.Tick(context.Background()); err == nil {
				t.Fatal("accepted stale controller response")
			}
			items, err := fake.List(context.Background())
			if err != nil || len(items) != 1 || items[0].ID != "tessera_keep" || !items[0].Running {
				t.Fatalf("stale response stopped running work: %+v %v", items, err)
			}
			cache, err := ag.readCache()
			if err != nil || cache.Epoch != 4 {
				t.Fatalf("fencing epoch not persisted: %+v %v", cache, err)
			}
		})
	}
}

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

func TestCorruptCacheCannotStartAgentWithFreshIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	body := []byte(`{"epoch":`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	ag := &Agent{DataDir: dir, Runtime: runtime.NewFake()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ag.Run(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("agent ignored corrupt fencing state: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != string(body) {
		t.Fatalf("agent replaced corrupt cache: %s %v", saved, err)
	}
}

type exitedOnStart struct{ *runtime.Fake }

func (r exitedOnStart) Start(ctx context.Context, spec runtime.Spec) (runtime.Container, error) {
	c, err := r.Fake.Start(ctx, spec)
	r.Fake.Kill(c.ID, "crash", false)
	c.Running = false
	return c, err
}

func TestImmediateExitCannotMarkReleaseHealthy(t *testing.T) {
	for _, kind := range []string{api.KindApp, api.KindJob} {
		t.Run(kind, func(t *testing.T) {
			var reports []client.StatusReport
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/assignments/fast-exit/status" {
					http.NotFound(w, r)
					return
				}
				var report client.StatusReport
				if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
					t.Error(err)
				}
				reports = append(reports, report)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			ag := &Agent{DataDir: t.TempDir(), Runtime: exitedOnStart{runtime.NewFake()}, Client: client.New(srv.URL, "token")}
			if err := ag.ConvergeForTest(context.Background(), []api.Assignment{{ID: "fast-exit", Image: "image", Kind: kind, Status: api.StatusPending}}, true); err != nil {
				t.Fatal(err)
			}
			want := api.StatusFailed
			if kind == api.KindJob {
				want = api.StatusSucceeded
			}
			if len(reports) != 1 || reports[0].Status != want {
				t.Fatalf("exited process marked healthy: %+v", reports)
			}
		})
	}
}
