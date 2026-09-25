package drill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
		{"route follows a move", routeFollowsMove},
		{"cached image skips the registry", cachedImage},
		{"backup restores onto a new leader", backupRestore},
		{"confirm wipe clears the node", confirmWipe},
		{"confirm reimage rejoins", confirmReimage},
		{"confirm delete removes the app", confirmDelete},
		{"confirm delete removes the node", confirmDeleteNode},
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

func backupRestore() error {
	srv, cl, cleanup := boot(time.Now())
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: restored\nimage: nginx\n")); err != nil {
		return err
	}
	root := tDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(root)
	backup := filepath.Join(root, "backup.db")
	if err := srv.Store.Backup(backup); err != nil {
		return err
	}
	oldID, oldEpoch := srv.ID, srv.Epoch()
	cleanup()
	cleanup = nil
	dir := filepath.Join(root, "replacement")
	if err := store.RestoreBackup(backup, filepath.Join(dir, "tessera.db"), 0); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dir, "tessera.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	next, err := controller.New(st, controller.Config{DataDir: dir})
	if err != nil {
		return err
	}
	if next.ID == oldID || next.Epoch() <= oldEpoch || next.Token != "drill-token" {
		return fmt.Errorf("restored identity id=%s epoch=%d token=%s", next.ID, next.Epoch(), next.Token)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer next.Close()
	go func() { _ = next.Serve(context.Background(), ln) }()
	clientNext := client.New("http://"+ln.Addr().String(), "drill-token")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		apps, err := clientNext.ListApps(context.Background())
		if err == nil {
			if len(apps) != 1 || apps[0].Name != "restored" {
				return fmt.Errorf("restored apps %+v", apps)
			}
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("replacement leader did not serve restored app")
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

func routeFollowsMove() error {
	now := time.Now()
	srv, cl, cleanup := boot(now)
	defer cleanup()
	aLn, aPort := banner("from-a")
	defer aLn.Close()
	bLn, bPort := banner("from-b")
	defer bLn.Close()
	port := freePort()
	body := fmt.Sprintf("kind: App\nname: web\nimage: nginx\nreplicas: 1\n---\nkind: Route\nname: web\napp: web\nport: %d\ntarget_port: 80\n", port)
	if err := cl.Apply(context.Background(), []byte(body)); err != nil {
		return err
	}
	if err := srv.Store.PutNode(api.Node{ID: "a", Addr: "127.0.0.1", Status: api.NodeReady, LastSeen: now}); err != nil {
		return err
	}
	if err := srv.Store.PutNode(api.Node{ID: "b", Addr: "127.0.0.1", Status: api.NodeReady, LastSeen: now}); err != nil {
		return err
	}
	old := api.Assignment{ID: "old", App: "web", NodeID: "a", Generation: 1, Status: api.StatusRunning, HostPort: aPort, Updated: now}
	if err := srv.Store.PutAssignment(old); err != nil {
		return err
	}
	if err := srv.Reconcile(now); err != nil {
		return err
	}
	if got, err := readPort(port); err != nil || got != "from-a" {
		return fmt.Errorf("before move %q %v", got, err)
	}
	neu := api.Assignment{ID: "new", App: "web", NodeID: "b", Generation: 1, Status: api.StatusRunning, HostPort: bPort, Replaces: "old", Updated: now.Add(time.Second)}
	if err := srv.Store.PutAssignment(neu); err != nil {
		return err
	}
	if err := srv.Reconcile(now.Add(time.Second)); err != nil {
		return err
	}
	if got, err := readPort(port); err != nil || got != "from-b" {
		return fmt.Errorf("after move %q %v", got, err)
	}
	return nil
}

func cachedImage() error {
	srv, cl, cleanup := boot(time.Now())
	defer cleanup()
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx\nreplicas: 1\n")); err != nil {
		return err
	}
	ag, fake := testAgent(cl, srv)
	if err := ag.Tick(context.Background()); err != nil {
		return err
	}
	if len(fake.Pulls) != 1 {
		return fmt.Errorf("first pulls %v", fake.Pulls)
	}
	items, err := fake.List(context.Background())
	if err != nil || len(items) != 1 {
		return fmt.Errorf("runtime %v %v", items, err)
	}
	fake.Kill(items[0].ID, "crash", false)
	if err := ag.Tick(context.Background()); err != nil {
		return err
	}
	if len(fake.Pulls) != 1 {
		return fmt.Errorf("second start pulled %v", fake.Pulls)
	}
	fake2 := runtime.NewFake()
	ag2 := &agent.Agent{
		ID: "node-b", DataDir: filepath.Join(tDir(), "agent-b"), Token: "drill-token",
		Client: cl, Runtime: fake2, Addr: "127.0.0.1",
	}
	if err := ag2.ConvergeForTest(context.Background(), []api.Assignment{{
		ID: "other", App: "web", Image: "nginx", Status: api.StatusRunning,
	}}, true); err != nil {
		return err
	}
	if len(fake2.Pulls) != 0 {
		return fmt.Errorf("cache miss %v", fake2.Pulls)
	}
	return nil
}

func confirmWipe() error {
	srv, cl, cleanup := boot(time.Now())
	defer cleanup()
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx\n")); err != nil {
		return err
	}
	ag, fake := testAgent(cl, srv)
	if err := ag.Tick(context.Background()); err != nil {
		return err
	}
	if items, _ := fake.List(context.Background()); len(items) != 1 {
		return fmt.Errorf("not running")
	}
	if _, err := cl.Act(context.Background(), "wipe", ag.ID, "clear"); err != nil {
		return err
	}
	id := actionID(cl, "wipe", ag.ID, "proposed")
	if id == "" {
		return fmt.Errorf("no proposed wipe")
	}
	got, err := cl.Confirm(context.Background(), id)
	if err != nil {
		return err
	}
	if got != "confirmed" {
		return fmt.Errorf("confirm %s", got)
	}
	if items, _ := fake.List(context.Background()); len(items) != 1 {
		return fmt.Errorf("wipe ran before the agent")
	}
	if err := ag.Tick(context.Background()); !errors.Is(err, agent.ErrHalted) {
		return fmt.Errorf("tick %v", err)
	}
	if items, _ := fake.List(context.Background()); len(items) != 0 {
		return fmt.Errorf("containers remain")
	}
	if _, err := os.Stat(filepath.Join(ag.DataDir, "cache.json")); !os.IsNotExist(err) {
		return fmt.Errorf("cache remains: %v", err)
	}
	if _, err := srv.Store.GetNode(ag.ID); err == nil {
		return fmt.Errorf("node remains")
	}
	if result := actionResult(cl, id); result != "done" {
		return fmt.Errorf("result %s", result)
	}
	return nil
}

func confirmReimage() error {
	srv, cl, cleanup := boot(time.Now())
	defer cleanup()
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx\n")); err != nil {
		return err
	}
	ag, fake := testAgent(cl, srv)
	if err := ag.Tick(context.Background()); err != nil {
		return err
	}
	cleared := false
	ag.Reexec = func() error {
		items, _ := fake.List(context.Background())
		if len(items) != 0 {
			return fmt.Errorf("containers remain")
		}
		b, err := os.ReadFile(filepath.Join(ag.DataDir, "cache.json"))
		if err != nil {
			return err
		}
		var c struct {
			NodeID      string           `json:"node_id"`
			Assignments []api.Assignment `json:"assignments"`
		}
		if err := json.Unmarshal(b, &c); err != nil || c.NodeID != ag.ID || len(c.Assignments) != 0 {
			return fmt.Errorf("identity %+v %v", c, err)
		}
		cleared = true
		return agent.ErrHalted
	}
	if _, err := cl.Act(context.Background(), "reimage", ag.ID, "rejoin"); err != nil {
		return err
	}
	id := actionID(cl, "reimage", ag.ID, "proposed")
	if id == "" {
		return fmt.Errorf("no proposed reimage")
	}
	if _, err := cl.Confirm(context.Background(), id); err != nil {
		return err
	}
	if err := ag.Tick(context.Background()); !errors.Is(err, agent.ErrHalted) {
		return fmt.Errorf("tick %v", err)
	}
	if !cleared {
		return fmt.Errorf("reexec did not run")
	}
	if _, err := srv.Store.GetNode(ag.ID); err != nil {
		return fmt.Errorf("node dropped: %v", err)
	}
	if result := actionResult(cl, id); result != "done" {
		return fmt.Errorf("result %s", result)
	}
	return nil
}

func confirmDelete() error {
	srv, cl, cleanup := boot(time.Now())
	defer cleanup()
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx\n")); err != nil {
		return err
	}
	ag, _ := testAgent(cl, srv)
	if err := ag.Tick(context.Background()); err != nil {
		return err
	}
	if _, err := cl.Act(context.Background(), "delete", "web", "remove"); err != nil {
		return err
	}
	id := actionID(cl, "delete", "web", "proposed")
	if id == "" {
		return fmt.Errorf("no proposed delete")
	}
	got, err := cl.Confirm(context.Background(), id)
	if err != nil {
		return err
	}
	if got != "done" {
		return fmt.Errorf("confirm %s", got)
	}
	apps, err := cl.ListApps(context.Background())
	if err != nil {
		return err
	}
	if len(apps) != 0 {
		return fmt.Errorf("app remains %+v", apps)
	}
	asgs, err := cl.ListAssignments(context.Background())
	if err != nil {
		return err
	}
	for _, a := range asgs {
		if a.App == "web" && a.Status != api.StatusStopped {
			return fmt.Errorf("assignment %s", a.Status)
		}
	}
	return nil
}

func confirmDeleteNode() error {
	srv, cl, cleanup := boot(time.Now())
	defer cleanup()
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx\n")); err != nil {
		return err
	}
	ag, fake := testAgent(cl, srv)
	if err := ag.Tick(context.Background()); err != nil {
		return err
	}
	if items, _ := fake.List(context.Background()); len(items) != 1 {
		return fmt.Errorf("workload not running before delete")
	}
	if _, err := cl.Act(context.Background(), "delete", ag.ID, "remove node"); err != nil {
		return err
	}
	id := actionID(cl, "delete", ag.ID, "proposed")
	if id == "" {
		return fmt.Errorf("no proposed node delete")
	}
	result, err := cl.Confirm(context.Background(), id)
	if err != nil || result != "confirmed" {
		return fmt.Errorf("confirmation %q: %v", result, err)
	}
	if _, err := srv.Store.GetNode(ag.ID); err != nil {
		return fmt.Errorf("node removed before agent stopped: %v", err)
	}
	if err := ag.Tick(context.Background()); !errors.Is(err, agent.ErrHalted) {
		return fmt.Errorf("agent did not halt: %v", err)
	}
	if _, err := srv.Store.GetNode(ag.ID); err == nil {
		return fmt.Errorf("node remains after agent stopped")
	}
	if items, err := fake.List(context.Background()); err != nil || len(items) != 0 {
		return fmt.Errorf("containers remain: %+v %v", items, err)
	}
	if result := actionResult(cl, id); result != "done" {
		return fmt.Errorf("result %s", result)
	}
	return nil
}

func actionID(cl *client.Client, kind, target, result string) string {
	actions, err := cl.ListActions(context.Background())
	if err != nil {
		return ""
	}
	for _, a := range actions {
		if a.Kind == kind && a.Target == target && a.Result == result {
			return a.ID
		}
	}
	return ""
}

func actionResult(cl *client.Client, id string) string {
	actions, err := cl.ListActions(context.Background())
	if err != nil {
		return ""
	}
	for _, a := range actions {
		if a.ID == id {
			return a.Result
		}
	}
	return ""
}

func banner(msg string) (net.Listener, int) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.WriteString(c, msg)
			}(c)
		}
	}()
	_, ps, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(ps)
	return ln, p
}

func freePort() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer ln.Close()
	_, ps, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(ps)
	return n
}

func readPort(port int) (string, error) {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	b, err := io.ReadAll(c)
	return string(b), err
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
