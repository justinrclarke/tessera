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
