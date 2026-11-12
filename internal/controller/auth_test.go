package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
)

func TestScopedNodeCannotReadOtherSecretsOrMutateOtherWork(t *testing.T) {
	s, admin := standaloneClient(t)
	ctx := context.Background()
	join, err := admin.JoinToken(ctx, "worker", "10m")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := client.New(admin.Endpoint(), join)
	response, err := bootstrap.Register(ctx, client.RegisterRequest{ID: "worker", Restricted: true})
	if err != nil || response.NodeToken == "" {
		t.Fatalf("join failed: %+v %v", response, err)
	}
	node := client.New(admin.Endpoint(), response.NodeToken)
	if _, err := bootstrap.Register(ctx, client.RegisterRequest{ID: "other"}); err == nil {
		t.Fatal("join token registered a different node")
	}
	if _, err := node.Register(ctx, client.RegisterRequest{ID: "other"}); err == nil {
		t.Fatal("node token registered a different node")
	}
	if err := s.Store.PutAssignment(api.Assignment{ID: "own", App: "app", NodeID: "worker", Status: api.StatusPending, Image: "image", Env: map[string]string{"SECRET": "own"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.PutAssignment(api.Assignment{ID: "foreign", App: "other", NodeID: "other", Status: api.StatusPending, Env: map[string]string{"SECRET": "foreign"}}); err != nil {
		t.Fatal(err)
	}
	page, err := node.WaitAssignments(ctx, "worker", 0, 0)
	if err != nil || len(page.Assignments) != 1 || page.Assignments[0].Env["SECRET"] != "own" {
		t.Fatalf("own assignments: %+v %v", page, err)
	}
	if _, err := node.WaitAssignments(ctx, "other", 0, 0); err == nil {
		t.Fatal("node read another node's environment")
	}
	if _, err := node.Snapshot(ctx); err == nil {
		t.Fatal("node read a full-cluster snapshot")
	}
	if _, err := node.ListApps(ctx); err == nil {
		t.Fatal("node read cluster apps")
	}
	if err := node.Apply(ctx, []byte("kind: App\nname: bad\nimage: bad\n")); err == nil {
		t.Fatal("node changed manifests")
	}
	if err := node.Report(ctx, "foreign", client.StatusReport{Status: api.StatusRunning}); err == nil {
		t.Fatal("node reported a foreign assignment")
	}
	if _, err := node.Act(ctx, "delete", "other", "attack"); err == nil {
		t.Fatal("node proposed cluster deletion")
	}
	if _, err := node.Heartbeat(ctx, "worker", client.HeartbeatRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := admin.RevokeNode(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := node.Heartbeat(ctx, "worker", client.HeartbeatRequest{}); err == nil {
		t.Fatal("revoked token remained valid")
	}
	if _, err := bootstrap.Register(ctx, client.RegisterRequest{ID: "worker", Restricted: true}); err == nil {
		t.Fatal("revoked node rejoined using its old bootstrap token")
	}
	fresh, err := admin.JoinToken(ctx, "worker", "10m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.New(admin.Endpoint(), fresh).Register(ctx, client.RegisterRequest{ID: "worker", Restricted: true}); err != nil {
		t.Fatalf("fresh authorized join failed after revocation: %v", err)
	}
}

func TestAPITokenRotationPreservesScopedCredentialsAndInvalidatesOldJoins(t *testing.T) {
	_, admin := standaloneClient(t)
	ctx := context.Background()
	join, err := admin.JoinToken(ctx, "worker", "10m")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.New(admin.Endpoint(), join).Register(ctx, client.RegisterRequest{ID: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	node := client.New(admin.Endpoint(), response.NodeToken)
	gatewayToken, err := admin.GatewayToken(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	gateway := client.New(admin.Endpoint(), gatewayToken)
	if _, err := gateway.ListApps(ctx); err == nil {
		t.Fatal("gateway credential could read workloads")
	}
	if _, err := gateway.RouteBackends(ctx, "another-route"); err == nil {
		t.Fatal("gateway credential accessed another route")
	}
	oldJoin, err := admin.JoinToken(ctx, "next", "10m")
	if err != nil {
		t.Fatal(err)
	}
	newToken := strings.Repeat("new-secret-", 6)
	if err := admin.RotateToken(ctx, newToken); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ControllerStatus(ctx); err == nil {
		t.Fatal("old API token still authorized")
	}
	if _, err := node.ControllerStatus(ctx); err != nil {
		t.Fatal("API rotation revoked a scoped worker")
	}
	if _, err := gateway.ControllerStatus(ctx); err != nil {
		t.Fatal("API rotation revoked a scoped gateway")
	}
	if _, err := client.New(admin.Endpoint(), oldJoin).Register(ctx, client.RegisterRequest{ID: "next"}); err == nil {
		t.Fatal("old join credential survived admin rotation")
	}
	if _, err := client.New(admin.Endpoint(), newToken).ControllerStatus(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestJoinTokenExpiresAndLegacyNodesBlockUnsafeRotation(t *testing.T) {
	s, admin := standaloneClient(t)
	ctx := context.Background()
	now := time.Now()
	s.Now = func() time.Time { return now }
	join, err := admin.JoinToken(ctx, "worker", "1s")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if _, err := client.New(admin.Endpoint(), join).Register(ctx, client.RegisterRequest{ID: "worker"}); err == nil {
		t.Fatal("expired join credential accepted")
	}
	if _, err := admin.Register(ctx, client.RegisterRequest{ID: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := admin.RotateToken(ctx, strings.Repeat("x", 64)); err == nil {
		t.Fatal("rotated while trusted agents retain promotion credentials")
	}
}

func TestScopedCredentialsAndRotationSurviveReplicatedElection(t *testing.T) {
	cluster := newReplicaTestCluster(t)
	leader := cluster.leader(t, -1)
	admin := client.New(cluster.peers[leader].URL, "replica-token")
	ctx := context.Background()
	join, err := admin.JoinToken(ctx, "worker", "10m")
	if err != nil {
		t.Fatal(err)
	}
	registration, err := client.New(admin.Endpoint(), join).Register(ctx, client.RegisterRequest{ID: "worker", Restricted: true})
	if err != nil || registration.NodeToken == "" {
		t.Fatalf("replicated scoped join: %+v %v", registration, err)
	}
	newToken := strings.Repeat("rotated-", 8)
	if err := admin.RotateToken(ctx, newToken); err != nil {
		t.Fatal(err)
	}
	waitReplica(t, func() bool {
		for _, server := range cluster.servers {
			if server.adminHash() != tokenHash(newToken) {
				return false
			}
		}
		return true
	})
	cluster.stop(leader)
	next := cluster.leader(t, leader)
	node := client.New(cluster.peers[next].URL, registration.NodeToken)
	if _, err := node.Heartbeat(ctx, "worker", client.HeartbeatRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.New(cluster.peers[next].URL, "replica-token").ListApps(ctx); err == nil {
		t.Fatal("election restored the revoked admin credential")
	}
	if _, err := node.Snapshot(ctx); err == nil {
		t.Fatal("election expanded scoped worker privileges")
	}
	if _, err := client.New(cluster.peers[next].URL, newToken).ListApps(ctx); err != nil {
		t.Fatal(err)
	}
}
