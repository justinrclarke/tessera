package drill

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tessera/internal/agent"
	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/controller"
	"tessera/internal/runtime"
	"tessera/internal/store"
)

type Report struct {
	Name   string
	Pass   bool
	Detail string
}

func Run() ([]Report, error) {
	steps := []struct {
		name string
		fn   func() error
	}{
		{"place and run", placeAndRun},
		{"dead node reschedules", deadNode},
		{"bad release rolls back", rollback},
		{"wipe stays proposed", wipeStaysProposed},
		{"old epoch is ignored", fence},
		{"cache restores without a controller", restoreCache},
		{"promote after leader loss", promote},
		{"faster node gets the workload", moveFast},
		{"certificate renews at half life", renewCert},
	}
	var out []Report
	for _, step := range steps {
		err := step.fn()
		rep := Report{Name: step.name, Pass: err == nil}
		if err != nil {
			rep.Detail = err.Error()
		}
		out = append(out, rep)
		if err != nil {
			return out, fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return out, nil
}

func placeAndRun() error {
	srv, cl, cleanup := boot(time.Now())
	defer cleanup()
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx\nreplicas: 1\n")); err != nil {
		return err
	}
	ag, fake := testAgent(cl, srv)
	if err := ag.Tick(context.Background()); err != nil {
		return err
	}
	asgs, err := cl.ListAssignments(context.Background())
	if err != nil {
		return err
	}
	if len(asgs) != 1 || asgs[0].Status != api.StatusRunning {
		return fmt.Errorf("assignments %+v", asgs)
	}
	if items, _ := fake.List(context.Background()); len(items) != 1 {
		return fmt.Errorf("runtime %+v", items)
	}
	return nil
}

func deadNode() error {
	now := time.Now()
	srv, cl, cleanup := boot(now)
	defer cleanup()
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx\n")); err != nil {
		return err
	}
	ag, _ := testAgent(cl, srv)
	if err := ag.Tick(context.Background()); err != nil {
		return err
	}
	deadAt := now.Add(time.Minute)
	other := api.Node{ID: "spare", Status: api.NodeReady, LastSeen: deadAt, Capacity: api.Resources{CPU: 4000, Memory: 1 << 30}, Perf: api.Perf{CPU: 1}}
	if err := srv.Store.PutNode(other); err != nil {
		return err
	}
	if err := srv.Reconcile(deadAt); err != nil {
		return err
	}
	asgs, err := cl.ListAssignments(context.Background())
	if err != nil {
		return err
	}
	var stopped, placed bool
	for _, a := range asgs {
		if a.Status == api.StatusStopped {
			stopped = true
		}
		if a.NodeID == "spare" && a.Status == api.StatusPending {
			placed = true
		}
	}
	if !stopped || !placed {
		return fmt.Errorf("assignments %+v", asgs)
	}
	return nil
}

func rollback() error {
	srv, cl, cleanup := boot(time.Now())
	defer cleanup()
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx:1\n")); err != nil {
		return err
	}
	ag, _ := testAgent(cl, srv)
	if err := ag.Tick(context.Background()); err != nil {
		return err
	}
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx:2\n")); err != nil {
		return err
	}
	asgs, err := cl.ListAssignments(context.Background())
	if err != nil {
		return err
	}
	var id string
	for _, a := range asgs {
		if a.Status != api.StatusStopped && a.Generation == 2 {
			id = a.ID
		}
	}
	if id == "" {
		return fmt.Errorf("no gen 2 assignment: %+v", asgs)
	}
	if err := cl.Report(context.Background(), id, client.StatusReport{Status: api.StatusFailed, Reason: "crash", Restarts: 1}); err != nil {
		return err
	}
	app, err := cl.GetApp(context.Background(), "web")
	if err != nil {
		return err
	}
	if app.Image != "nginx:1" {
		return fmt.Errorf("image %s generation %d", app.Image, app.Generation)
	}
	_ = srv
	return nil
}

func wipeStaysProposed() error {
	_, cl, cleanup := boot(time.Now())
	defer cleanup()
	result, err := cl.Act(context.Background(), "wipe", "n1", "disk destroyed")
	if err != nil {
		return err
	}
	if !strings.Contains(result, "proposed") {
		return fmt.Errorf("response %s", result)
	}
	actions, err := cl.ListActions(context.Background())
	if err != nil {
		return err
	}
	for _, a := range actions {
		if a.Kind == "wipe" && a.Result == "done" {
			return fmt.Errorf("wipe executed")
		}
	}
	return nil
}

func fence() error {
	fake := runtime.NewFake()
	ag := &agent.Agent{ID: "n1", DataDir: filepath.Join(tDir(), "fence"), Runtime: fake, MaxRestarts: 3}
	ag.SetEpoch(3)
	err := ag.ConvergeForTest(context.Background(), []api.Assignment{{
		ID: "old", App: "web", Image: "nginx", Epoch: 2, Status: api.StatusPending, Generation: 1,
	}}, true)
	if err != nil {
		return err
	}
	items, _ := fake.List(context.Background())
	if len(items) != 0 {
		return fmt.Errorf("started fenced assignment: %+v", items)
	}
	return nil
}

func restoreCache() error {
	dir := filepath.Join(tDir(), "restore")
	fake := runtime.NewFake()
	ag := &agent.Agent{ID: "n1", DataDir: dir, Runtime: fake}
	asg := api.Assignment{ID: "keep", App: "web", Image: "nginx", Status: api.StatusRunning, Epoch: 1, Generation: 1}
	if err := ag.SaveForTest([]api.Assignment{asg}); err != nil {
		return err
	}
	ag2 := &agent.Agent{DataDir: dir, Runtime: runtime.NewFake()}
	if err := ag2.Tick(context.Background()); err != nil {
		return err
	}
	items, _ := ag2.Runtime.List(context.Background())
	if len(items) != 1 {
		return fmt.Errorf("not restored: %+v", items)
	}
	return nil
}

func promote() error {
	now := time.Now()
	srv, cl, cleanup := boot(now)
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx\n")); err != nil {
		cleanup()
		return err
	}
	ag, _ := testAgent(cl, srv)
	if err := ag.Tick(context.Background()); err != nil {
		cleanup()
		return err
	}
	if err := ag.FetchForTest(context.Background()); err != nil {
		cleanup()
		return err
	}
	cleanup()
	ag.Client = client.New("http://127.0.0.1:1", ag.Token)
	ag.LostAt = now.Add(-time.Minute)
	ag.Now = func() time.Time { return now }
	ok, err := ag.PromoteForTest(context.Background())
	if err != nil || !ok {
		return fmt.Errorf("promote %v %w", ok, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var last error
	for ctx.Err() == nil {
		apps, err := ag.Client.ListApps(context.Background())
		if err == nil && len(apps) == 1 && apps[0].Name == "web" {
			return nil
		}
		last = err
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("promoted controller missing app: %v", last)
}

func moveFast() error {
	now := time.Now()
	srv, cl, cleanup := boot(now)
	defer cleanup()
	body := []byte("kind: Policy\nmove:\n  min_gain: 0.15\n  cooldown: 1s\nkind: App\nname: web\nimage: nginx\nsensitive_to: cpu\n")
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx\nsensitive_to: cpu\n")); err != nil {
		return err
	}
	_ = body
	ag, _ := testAgent(cl, srv)
	if err := ag.Tick(context.Background()); err != nil {
		return err
	}
	fast := api.Node{
		ID: "fast", Status: api.NodeReady, LastSeen: now,
		Capacity: api.Resources{CPU: 8000, Memory: 1 << 30},
		Perf:     api.Perf{CPU: 5, Memory: 5, Disk: 5},
	}
	if err := srv.Store.PutNode(fast); err != nil {
		return err
	}
	slow, err := srv.Store.GetNode(ag.ID)
	if err != nil {
		return err
	}
	slow.Perf = api.Perf{CPU: 1, Memory: 1, Disk: 1}
	slow.LastSeen = now
	if err := srv.Store.PutNode(slow); err != nil {
		return err
	}
	if err := srv.Reconcile(now.Add(time.Second)); err != nil {
		return err
	}
	asgs, err := cl.ListAssignments(context.Background())
	if err != nil {
		return err
	}
	var moved bool
	for _, a := range asgs {
		if a.NodeID == "fast" && a.Replaces != "" {
			moved = true
		}
	}
	if !moved {
		return fmt.Errorf("no move: %+v", asgs)
	}
	return nil
}

func renewCert() error {
	now := time.Now()
	srv, cl, cleanup := boot(now)
	defer cleanup()
	ag, _ := testAgent(cl, srv)
	if err := ag.Tick(context.Background()); err != nil {
		return err
	}
	n, err := srv.Store.GetNode(ag.ID)
	if err != nil {
		return err
	}
	n.CertNotBefore = now.Add(-2 * time.Hour)
	n.CertNotAfter = now.Add(20 * time.Minute)
	if err := srv.Store.PutNode(n); err != nil {
		return err
	}
	hb, err := cl.Heartbeat(context.Background(), ag.ID, client.HeartbeatRequest{Addr: "127.0.0.1"})
	if err != nil {
		return err
	}
	if hb.CertPEM == "" {
		return fmt.Errorf("cert was not renewed")
	}
	return nil
}

func boot(now time.Time) (*controller.Server, *client.Client, func()) {
	dir := tDir()
	st, err := store.Open(filepath.Join(dir, "tessera.db"))
	if err != nil {
		panic(err)
	}
	srv, err := controller.New(st, controller.Config{DataDir: dir, Token: "drill-token", ID: "leader"})
	if err != nil {
		panic(err)
	}
	srv.Now = func() time.Time { return now }
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() { _ = srv.Serve(context.Background(), ln) }()
	cl := client.New("http://"+ln.Addr().String(), "drill-token")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cl.Health(context.Background()) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return srv, cl, func() {
		_ = srv.Close()
		_ = st.Close()
	}
}

func testAgent(cl *client.Client, srv *controller.Server) (*agent.Agent, *runtime.Fake) {
	fake := runtime.NewFake()
	ag := &agent.Agent{
		ID: "node-a", DataDir: filepath.Join(tDir(), "agent"), Token: "drill-token",
		Client: cl, Runtime: fake, Addr: "127.0.0.1", MaxRestarts: 3,
	}
	_ = srv
	return ag, fake
}

func tDir() string {
	return filepath.Join(os.TempDir(), "tessera-drill-"+api.NewID())
}
