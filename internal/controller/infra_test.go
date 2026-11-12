package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tessera/internal/client"
	"tessera/internal/cloud"
	"tessera/internal/store"
)

type fakeCloud struct {
	mu                      sync.Mutex
	networks, machines      map[string]cloud.Resource
	creates, stops, deletes int
	loseResponse            bool
}

func (f *fakeCloud) Discover(context.Context) ([]cloud.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []cloud.Instance
	for _, r := range f.machines {
		out = append(out, cloud.Instance{ID: r.ID, Key: r.Key, Name: r.Name})
	}
	return out, nil
}
func (f *fakeCloud) CreateNetwork(_ context.Context, s cloud.NetworkSpec) (cloud.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.networks == nil {
		f.networks = map[string]cloud.Resource{}
	}
	if r, ok := f.networks[s.Key]; ok {
		return r, nil
	}
	r := cloud.Resource{ID: "network-" + s.Key, Subnet: "subnet-" + s.Key, Name: s.Name, Region: s.Region, Key: s.Key}
	f.networks[s.Key] = r
	return r, nil
}
func (f *fakeCloud) Create(_ context.Context, s cloud.MachineSpec) (cloud.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.machines == nil {
		f.machines = map[string]cloud.Resource{}
	}
	if r, ok := f.machines[s.Key]; ok {
		return r, nil
	}
	r := cloud.Resource{ID: "instance-" + s.Key, Name: s.Name, Region: s.Region, Key: s.Key}
	f.machines[s.Key] = r
	f.creates++
	if f.loseResponse {
		f.loseResponse = false
		return r, fmt.Errorf("lost provider response")
	}
	return r, nil
}
func (f *fakeCloud) Inspect(_ context.Context, r cloud.Resource) (cloud.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.machines {
		if m.ID == r.ID {
			return cloud.Instance{ID: m.ID, Key: m.Key, Name: m.Name}, nil
		}
	}
	return cloud.Instance{}, cloud.ErrNotFound
}
func (f *fakeCloud) Stop(_ context.Context, r cloud.Resource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Key == "" {
		return fmt.Errorf("unowned")
	}
	delete(f.machines, r.Key)
	f.stops++
	return nil
}
func (f *fakeCloud) DeleteNetwork(_ context.Context, r cloud.Resource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Key == "" {
		return fmt.Errorf("unowned")
	}
	delete(f.networks, r.Key)
	f.deletes++
	return nil
}

func (f *fakeCloud) InspectNetwork(_ context.Context, r cloud.Resource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.networks[r.Key]; !ok {
		return cloud.ErrNotFound
	}
	return nil
}

func TestInfrastructureResumesLostResponseWaitsForJoinAndRequiresConfirm(t *testing.T) {
	s, c := standaloneClient(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	s.URL = "http://controller.test:7468"
	f := &fakeCloud{loseResponse: true}
	s.CloudFactory = func(string, cloud.Settings) cloud.Provider { return f }
	body := []byte("networks:\n- {name: private, provider: aws, region: us-east-1, cidr: 10.0.0.0/24}\nmachines:\n- {name: worker, provider: aws, region: us-east-1, type: t3.small, image: ami-agent, network: private}\napps:\n- {name: web, image: web, replicas: 0}\n")
	ctx := context.Background()
	if _, err := c.InfrastructureApply(ctx, body); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileInfrastructure(ctx); err == nil {
		t.Fatal("lost provider response was hidden")
	}
	state, _ := readInfra(s.Store)
	if state.Pending == nil || len(state.State.Machines) != 0 || len(f.machines) != 1 {
		t.Fatalf("missing durable operation: %+v", state)
	}
	now = now.Add(3 * time.Second)
	if err := s.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatalf("retried create duplicated instances: %d", f.creates)
	}
	if err := s.ReconcileInfrastructure(ctx); err == nil {
		t.Fatal("applied before agent joined")
	}
	if apps, _ := s.Store.ListApps(); len(apps) != 0 {
		t.Fatal("app bypassed node readiness")
	}
	state, _ = readInfra(s.Store)
	machine := state.State.Machines[0]
	if _, err := c.Register(ctx, client.RegisterRequest{ID: machine.NodeID}); err != nil {
		t.Fatal(err)
	}
	node, _ := s.Store.GetNode(machine.NodeID)
	if node.Cloud == nil || node.Cloud.ID != machine.Resource.ID || !node.Cloud.Owned {
		t.Fatalf("missing node cloud identity: %+v", node)
	}
	now = now.Add(3 * time.Second)
	for range 3 {
		if err := s.ReconcileInfrastructure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	state, _ = readInfra(s.Store)
	if state.Phase != "ready" {
		t.Fatalf("not ready: %+v", state)
	}
	if _, err := c.InfrastructureApply(ctx, body); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.ReconcileInfrastructure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f.creates != 1 {
		t.Fatal("second apply recreated machine")
	}
	if _, err := c.InfrastructureApply(ctx, []byte("networks: []\nmachines: []\napps: []\n")); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.ReconcileInfrastructure(ctx); err != nil {
			t.Fatal(err)
		}
		actions, _ := s.Store.ListActions(100)
		id := ""
		for _, a := range actions {
			if a.Result == "proposed" {
				id = a.ID
			}
		}
		if id == "" {
			t.Fatal("removal was not proposed")
		}
		if _, err := c.Confirm(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := s.ReconcileInfrastructure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f.stops != 1 || f.deletes != 1 {
		t.Fatalf("confirmed removals: stops=%d networks=%d", f.stops, f.deletes)
	}
}

func TestInfrastructureNeverRemovesAdoptedMachine(t *testing.T) {
	s, c := standaloneClient(t)
	f := &fakeCloud{machines: map[string]cloud.Resource{"external": {ID: "external-vm", Name: "external"}}}
	s.CloudFactory = func(string, cloud.Settings) cloud.Provider { return f }
	ctx := context.Background()
	body := []byte("machines:\n- {name: adopted, provider: aws, region: us-east-1, image: existing, type: t3.small, adopt: external-vm, node_id: existing-node}\n")
	if _, err := c.InfrastructureApply(ctx, body); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InfrastructureApply(ctx, []byte("machines: []\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	if f.stops != 0 || len(f.machines) != 1 {
		t.Fatal("adopted VM was modified")
	}
}

func TestInfrastructureRollbackPreservesExactUnitsAndExternalAppOwnership(t *testing.T) {
	s, c := standaloneClient(t)
	ctx := context.Background()
	if err := c.Apply(ctx, []byte("kind: App\nname: external\nimage: original\nreplicas: 0\n")); err != nil {
		t.Fatal(err)
	}
	first := []byte("apps:\n- {name: external, image: original, replicas: 0, resources: {cpu: 1m}}\n")
	if _, err := c.InfrastructureApply(ctx, first); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.ReconcileInfrastructure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := readInfra(s.Store)
	if state.State.Apps[0].Owned {
		t.Fatal("infrastructure claimed ownership of a preexisting App")
	}
	if _, err := c.InfrastructureApply(ctx, []byte("apps:\n- {name: external, image: updated, replicas: 0, resources: {cpu: 2m}}\n")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.ReconcileInfrastructure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.InfrastructureRollback(ctx, ""); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.ReconcileInfrastructure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	app, _ := s.Store.GetApp("external")
	if app.Image != "original" || app.Resources.CPU != 1 {
		t.Fatalf("rollback changed original intent: %+v", app)
	}
	if _, err := c.InfrastructureApply(ctx, []byte("apps: []\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.GetApp("external"); err != nil {
		t.Fatal("removing infrastructure intent deleted an external App")
	}
}

func TestInfrastructureSnapshotResumesAmbiguousCreateWithoutDuplicate(t *testing.T) {
	s, c := standaloneClient(t)
	ctx := context.Background()
	f := &fakeCloud{loseResponse: true}
	s.CloudFactory = func(string, cloud.Settings) cloud.Provider { return f }
	body := []byte("networks:\n- {name: net, provider: aws, region: us-east-1, cidr: 10.0.0.0/24}\nmachines:\n- {name: worker, provider: aws, region: us-east-1, type: small, image: prebuilt, network: net}\n")
	if _, err := c.InfrastructureApply(ctx, body); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileInfrastructure(ctx); err == nil {
		t.Fatal("expected ambiguous provider result")
	}
	snap, _, err := s.Store.LatestSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.LoadSnapshot(snap, s.Epoch()+1); err != nil {
		t.Fatal(err)
	}
	restored, err := New(st, Config{DataDir: t.TempDir(), Token: s.Token})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restored.Now = func() time.Time { return time.Now().Add(time.Minute) }
	restored.CloudFactory = s.CloudFactory
	if err := restored.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := readInfra(st)
	if state.Pending != nil || len(state.State.Machines) != 1 || f.creates != 1 || len(f.networks) != 1 {
		t.Fatalf("restore duplicated resources or lost operation: %+v creates=%d", state, f.creates)
	}
	delete(f.machines, state.State.Machines[0].Resource.Key)
	if err := restored.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restored.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restored.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ = readInfra(st)
	if f.creates != 2 || state.State.Machines[0].Resource.Key == snapKey(t, snap.Infrastructure["infra:state"]) {
		t.Fatal("lost owned VM did not receive a fresh provider idempotency key")
	}
}

func snapKey(t *testing.T, raw string) string {
	t.Helper()
	var state InfraState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatal(err)
	}
	return pendingInfraKey(state)
}

func TestInfrastructureAbandonRequiresConfirmAndPreservesResourceAudit(t *testing.T) {
	s, c := standaloneClient(t)
	ctx := context.Background()
	f := &fakeCloud{loseResponse: true}
	s.CloudFactory = func(string, cloud.Settings) cloud.Provider { return f }
	if _, err := c.InfrastructureApply(ctx, []byte("machines:\n- {name: worker, provider: aws, region: us-east-1, type: small, image: prebuilt}\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileInfrastructure(ctx); err == nil {
		t.Fatal("expected pending operation")
	}
	if _, err := c.InfrastructureAbandon(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := readInfra(s.Store)
	if state.Pending == nil || len(state.Abandoned) != 0 {
		t.Fatal("unconfirmed abandonment changed intent")
	}
	actions, _ := s.Store.ListActions(100)
	if len(actions) != 1 || actions[0].Result != "proposed" {
		t.Fatalf("missing proposal: %+v", actions)
	}
	if _, err := c.Confirm(ctx, actions[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ = readInfra(s.Store)
	if state.Phase != "cancelled" || state.Pending != nil || len(state.Abandoned) != 1 || state.Abandoned[0].OwnershipKey == "" || len(f.machines) != 1 || f.stops != 0 {
		t.Fatalf("abandonment lost audit or removed a VM: %+v", state)
	}
}

func TestInfrastructureLeaderLossResumesCommittedProviderOperation(t *testing.T) {
	f := &fakeCloud{loseResponse: true}
	cluster := newReplicaTestClusterTransport(t, false, func(s *Server) {
		s.CloudFactory = func(string, cloud.Settings) cloud.Provider { return f }
	})
	leader := cluster.leader(t, -1)
	server := cluster.servers[leader]
	admin := client.New(cluster.peers[leader].URL, "replica-token")
	ctx := context.Background()
	if _, err := admin.InfrastructureApply(ctx, []byte("machines:\n- {name: worker, provider: aws, region: us-east-1, type: small, image: prebuilt}\n")); err != nil {
		t.Fatal(err)
	}
	_ = server.ReconcileInfrastructure(ctx)
	waitReplica(t, func() bool {
		for _, s := range cluster.servers {
			state, err := readInfra(s.Store)
			if err != nil || state.Pending == nil || state.Phase != "error" {
				return false
			}
		}
		return true
	})
	cluster.stop(leader)
	next := cluster.leader(t, leader)
	server = cluster.servers[next]
	state, _ := readInfra(server.Store)
	if delay := time.Until(state.RetryAfter); delay > 0 {
		time.Sleep(delay + 10*time.Millisecond)
	}
	if err := server.ReconcileInfrastructure(ctx); err != nil {
		t.Fatal(err)
	}
	waitReplica(t, func() bool {
		state, _ = readInfra(server.Store)
		return state.Pending == nil && len(state.State.Machines) == 1
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.creates != 1 || len(f.machines) != 1 {
		t.Fatalf("election lost operation identity: %+v creates=%d", state, f.creates)
	}
}

func TestInfrastructureMachineRollbackUsesFreshProviderKey(t *testing.T) {
	s, c := standaloneClient(t)
	ctx := context.Background()
	f := &fakeCloud{}
	s.CloudFactory = func(string, cloud.Settings) cloud.Provider { return f }
	manifest := func(size string) []byte {
		return []byte(fmt.Sprintf("machines:\n- {name: worker, provider: aws, region: us-east-1, type: %s, image: prebuilt}\n", size))
	}
	if _, err := c.InfrastructureApply(ctx, manifest("small")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.ReconcileInfrastructure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := readInfra(s.Store)
	if _, err := c.InfrastructureApply(ctx, manifest("large")); err != nil {
		t.Fatal(err)
	}
	settle := func() {
		t.Helper()
		if err := s.ReconcileInfrastructure(ctx); err != nil {
			t.Fatal(err)
		}
		actions, _ := s.Store.ListActions(100)
		for _, action := range actions {
			if action.Result == "proposed" {
				if _, err := c.Confirm(ctx, action.ID); err != nil {
					t.Fatal(err)
				}
			}
		}
		for range 3 {
			if err := s.ReconcileInfrastructure(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	settle()
	if _, err := c.InfrastructureRollback(ctx, ""); err != nil {
		t.Fatal(err)
	}
	settle()
	last, _ := readInfra(s.Store)
	if len(last.State.Machines) != 1 || last.State.Machines[0].Machine.Type != "small" || last.State.Machines[0].Resource.Key == first.State.Machines[0].Resource.Key || f.creates != 3 || f.stops != 2 {
		t.Fatalf("rollback reused a terminated provider request: %+v creates=%d stops=%d", last, f.creates, f.stops)
	}
}
