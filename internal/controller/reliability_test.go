package controller

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/store"
)

func standaloneClient(t *testing.T) (*Server, *client.Client) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "tessera.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, Config{DataDir: dir, Token: "test-token"})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { httpServer.Close(); srv.Close(); st.Close() })
	return srv, client.New(httpServer.URL, srv.Token)
}

func TestRegistrationPreservesCordonUntilCooldown(t *testing.T) {
	srv, cl := standaloneClient(t)
	now := time.Now()
	srv.Now = func() time.Time { return now }
	if err := srv.Store.PutNode(api.Node{ID: "node", Status: api.NodeCordoned, CordonedAt: now, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Register(context.Background(), client.RegisterRequest{ID: "node"}); err != nil {
		t.Fatal(err)
	}
	node, err := srv.Store.GetNode("node")
	if err != nil || node.Status != api.NodeCordoned || !node.CordonedAt.Equal(now) {
		t.Fatalf("registration cleared cordon: %+v %v", node, err)
	}
	now = now.Add(11 * time.Minute)
	if _, err := cl.Register(context.Background(), client.RegisterRequest{ID: "node"}); err != nil {
		t.Fatal(err)
	}
	node, err = srv.Store.GetNode("node")
	if err != nil || node.Status != api.NodeReady || !node.CordonedAt.IsZero() {
		t.Fatalf("cooldown did not uncordon fresh node: %+v %v", node, err)
	}
}

func TestLateStatusCannotResurrectTerminalAssignment(t *testing.T) {
	for _, status := range []string{api.StatusStopped, api.StatusSucceeded} {
		t.Run(status, func(t *testing.T) {
			srv, cl := standaloneClient(t)
			assignment := api.Assignment{ID: "old", Status: status, Epoch: srv.Epoch()}
			if err := srv.Store.PutAssignment(assignment); err != nil {
				t.Fatal(err)
			}
			if err := cl.Report(context.Background(), assignment.ID, client.StatusReport{Epoch: assignment.Epoch, Status: api.StatusRunning}); err == nil {
				t.Fatal("late running report was accepted")
			}
			stored, err := srv.Store.GetAssignment(assignment.ID)
			if err != nil || stored.Status != status {
				t.Fatalf("resurrected terminal assignment: %+v %v", stored, err)
			}
		})
	}
}

func TestReplicasCanShareOneNodeAndScale(t *testing.T) {
	srv, cl := standaloneClient(t)
	if _, err := cl.Register(context.Background(), client.RegisterRequest{ID: "node"}); err != nil {
		t.Fatal(err)
	}
	for _, replicas := range []string{"2", "3", "0"} {
		if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: nginx\nreplicas: "+replicas+"\n")); err != nil {
			t.Fatal(err)
		}
		assignments, err := srv.Store.ListAssignments()
		active := 0
		for _, assignment := range assignments {
			if api.Active(assignment.Status) {
				active++
			}
		}
		if err != nil || active != int(replicas[0]-'0') {
			t.Fatalf("replicas=%s got %d active assignments: %v", replicas, active, err)
		}
		if err := srv.Reconcile(time.Now()); err != nil {
			t.Fatal(err)
		}
		again, err := srv.Store.ListAssignments()
		if err != nil || len(again) != len(assignments) {
			t.Fatalf("stable reconciliation added work: %+v %v", again, err)
		}
	}
}

func TestConfigAndSecretRolloutRetainsRollbackValues(t *testing.T) {
	srv, cl := standaloneClient(t)
	ctx := context.Background()
	if _, err := cl.Register(ctx, client.RegisterRequest{ID: "node"}); err != nil {
		t.Fatal(err)
	}
	body := "kind: App\nname: web\nimage: nginx\nconfigs: [settings]\nsecrets: [credentials]\n---\nkind: Config\nname: settings\ndata: {MODE: old}\n---\nkind: Secret\nname: credentials\ndata: {PASSWORD: original}\n"
	if err := cl.Apply(ctx, []byte(body)); err != nil {
		t.Fatal(err)
	}
	asgs, _ := srv.Store.ListAssignments()
	if len(asgs) != 1 || asgs[0].Env["MODE"] != "old" || asgs[0].Env["PASSWORD"] != "original" {
		t.Fatalf("unresolved release: %+v", asgs)
	}
	if err := cl.Report(ctx, asgs[0].ID, client.StatusReport{Status: api.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	for _, generation := range []int64{2, 3} {
		if err := cl.Apply(ctx, []byte("kind: Secret\nname: credentials\ndata: {PASSWORD: changed}\n")); err != nil {
			t.Fatal(err)
		}
		app, _ := cl.GetApp(ctx, "web")
		if app.Generation != generation || app.ReleaseEnv != nil {
			t.Fatalf("generation or public secret exposure: %+v", app)
		}
		asgs, _ = srv.Store.ListAssignments()
		id := ""
		for _, a := range asgs {
			if a.Generation == generation && api.Active(a.Status) {
				id = a.ID
				if a.Env["PASSWORD"] != "changed" {
					t.Fatalf("new release env: %+v", a)
				}
			}
		}
		if id == "" {
			t.Fatalf("no replacement generation %d", generation)
		}
		if err := cl.Report(ctx, id, client.StatusReport{Status: api.StatusFailed, Reason: "crash"}); err != nil {
			t.Fatal(err)
		}
		app, _ = srv.Store.GetApp("web")
		if app.Generation != 1 || app.ReleaseEnv["PASSWORD"] != "original" {
			t.Fatalf("rollback lost immutable secret: %+v", app)
		}
	}
	asgs, _ = srv.Store.ListAssignments()
	running := 0
	for _, a := range asgs {
		if a.Status == api.StatusRunning && a.Generation == 1 && a.Env["PASSWORD"] == "original" {
			running++
		}
	}
	if running != 1 {
		t.Fatalf("config failures stopped healthy service: %+v", asgs)
	}
}

func TestApplyFailureIsAtomic(t *testing.T) {
	srv, cl := standaloneClient(t)
	ctx := context.Background()
	if err := cl.Apply(ctx, []byte("kind: Config\nname: settings\ndata: {MODE: original}\n")); err != nil {
		t.Fatal(err)
	}
	body := "kind: Config\nname: settings\ndata: {MODE: changed}\n---\nkind: App\nname: web\nimage: nginx\nconfigs: [missing]\n"
	if err := cl.Apply(ctx, []byte(body)); err == nil {
		t.Fatal("missing config was accepted")
	}
	settings, _ := srv.Store.GetConfig("settings")
	apps, _ := srv.Store.ListApps()
	if settings.Data["MODE"] != "original" || len(apps) != 0 {
		t.Fatalf("failed apply partially committed: %+v %+v", settings, apps)
	}
}

func TestGenerationBecomesHealthyOnlyAfterAllReplicasAreReady(t *testing.T) {
	srv, cl := standaloneClient(t)
	ctx := context.Background()
	if _, err := cl.Register(ctx, client.RegisterRequest{ID: "node"}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Apply(ctx, []byte("kind: App\nname: web\nimage: nginx\nreplicas: 2\n")); err != nil {
		t.Fatal(err)
	}
	asgs, _ := srv.Store.ListAssignments()
	if err := cl.Report(ctx, asgs[0].ID, client.StatusReport{Status: api.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	app, _ := srv.Store.GetApp("web")
	if app.HealthyGeneration != 0 {
		t.Fatal("one ready replica marked the entire generation healthy")
	}
	if err := cl.Report(ctx, asgs[1].ID, client.StatusReport{Status: api.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	app, _ = srv.Store.GetApp("web")
	if app.HealthyGeneration != 1 {
		t.Fatalf("ready generation not saved: %+v", app)
	}
}
