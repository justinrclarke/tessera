package schedule

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"tessera/internal/api"
)

func TestPlacementBlockersUseSchedulingConstraints(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*api.App, *api.Node)
		want   string
	}{
		{"cordoned", func(a *api.App, n *api.Node) { n.Status = api.NodeCordoned }, "node is cordoned"},
		{"labels", func(a *api.App, n *api.Node) { a.NodeLabels = map[string]string{"storage": "shared", "fabric": "fast"} }, "requires node label fabric=fast"},
		{"cpu", func(a *api.App, n *api.Node) { a.Resources.CPU = 2000 }, "needs 2000m CPU; 1000m available"},
		{"memory capacity", func(a *api.App, n *api.Node) { a.Resources.Memory = 2 << 30 }, "available after reservations"},
		{"free memory", func(a *api.App, n *api.Node) { n.Free.Memory = 1 }, "node reports 1 free"},
		{"GPU count", func(a *api.App, n *api.Node) { a.GPUs = 1 }, "needs 1 GPUs; 0 available"},
		{"GPU inventory", func(a *api.App, n *api.Node) { a.GPUs, n.GPUs, a.GPUModel = 1, 1, "H100" }, "GPU inventory is unavailable"},
		{"GPU model", func(a *api.App, n *api.Node) {
			a.GPUs, n.GPUs, a.GPUModel = 1, 1, "H100"
			n.GPUInventory = []api.GPU{{UUID: "gpu-a", Model: "A100", MemoryFree: 20 << 30}}
		}, `of model "H100"`},
		{"GPU memory", func(a *api.App, n *api.Node) {
			a.GPUs, n.GPUs, a.GPUMemory = 1, 1, 8<<30
			n.GPUInventory = []api.GPU{{UUID: "gpu-a", MemoryFree: 4 << 30}}
		}, "free memory per device"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, n := app("web", 1), node("worker", api.NodeReady, 1, 1000)
			test.change(&a, &n)
			in := Input{Apps: []api.App{a}, Nodes: []api.Node{n}}
			before, _ := json.Marshal(in)
			got := Plan(in)
			after, _ := json.Marshal(in)
			if string(before) != string(after) {
				t.Fatal("planner changed its input")
			}
			if len(got.Place) != 0 || len(got.Blocked) != 1 || got.Blocked[0].Missing != 1 || len(got.Blocked[0].Nodes) != 1 || !strings.Contains(got.Blocked[0].Nodes[0].Reason, test.want) {
				t.Fatalf("want %s: %+v", test.want, got)
			}
		})
	}
}

func TestPlacementExplainsPartialCapacityAndStableNodeOrder(t *testing.T) {
	a := app("web", 2)
	a.Resources.CPU = 1000
	in := Input{Apps: []api.App{a}, Nodes: []api.Node{node("ready", api.NodeReady, 1, 1000), node("cordoned", api.NodeCordoned, 2, 4000)}}
	got := Plan(in)
	if len(got.Place) != 1 || len(got.Blocked) != 1 || got.Blocked[0].Missing != 1 {
		t.Fatalf("partial capacity: %+v", got)
	}
	if got.Blocked[0].Nodes[0].NodeID != "cordoned" || !strings.Contains(got.Blocked[0].Nodes[1].Reason, "0m available after reservations") {
		t.Fatalf("missing reservations: %+v", got.Blocked)
	}
	in.Nodes[0], in.Nodes[1] = in.Nodes[1], in.Nodes[0]
	if again := Plan(in); !reflect.DeepEqual(again, got) {
		t.Fatalf("node order changed diagnosis: %+v %+v", got, again)
	}
}

func TestPlacementExplainsReservedGPUAndHostPort(t *testing.T) {
	a := app("web", 2)
	a.GPUs = 1
	n := node("worker", api.NodeReady, 1, 1000)
	n.GPUs = 2
	n.GPUInventory = []api.GPU{{UUID: "h100", Model: "H100"}, {UUID: "a100", Model: "A100"}}
	a.GPUModel = "H100"
	in := Input{Apps: []api.App{a}, Nodes: []api.Node{n}, Assignments: []api.Assignment{{ID: "old", App: "web", NodeID: "worker", Generation: 1, Status: api.StatusRunning, GPUs: 1, GPUDevices: []string{"h100"}}}}
	got := Plan(in)
	if len(got.Place) != 0 || len(got.Blocked) != 1 || !strings.Contains(got.Blocked[0].Nodes[0].Reason, "unreserved GPU devices") {
		t.Fatalf("reserved matching GPU was reused: %+v", got)
	}
	in.Apps[0].GPUs, in.Apps[0].GPUModel = 0, ""
	in.Apps[0].Ports = []api.Port{{Container: 80, Host: 8080}}
	in.Assignments[0].Ports = in.Apps[0].Ports
	got = Plan(in)
	if len(got.Place) != 0 || len(got.Blocked) != 1 || got.Blocked[0].Nodes[0].Reason != "host port 8080 is already reserved" {
		t.Fatalf("missing host-port reservation: %+v", got)
	}
}

func TestPlacementExplainsRolloutWithoutStoppingOldGeneration(t *testing.T) {
	a := app("web", 1)
	a.Generation, a.Resources.CPU = 2, 1000
	in := Input{Apps: []api.App{a}, Nodes: []api.Node{node("full", api.NodeReady, 1, 1000)}, Assignments: []api.Assignment{{ID: "old", App: "web", NodeID: "full", Generation: 1, Status: api.StatusRunning, Resources: a.Resources}}}
	got := Plan(in)
	if len(got.Stop) != 0 || len(got.Place) != 0 || len(got.Blocked) != 1 || !got.Blocked[0].Replacement || !strings.Contains(got.Blocked[0].Reason, "previous generation stays running") {
		t.Fatalf("blocked rollout: %+v", got)
	}
	in.Nodes = append(in.Nodes, node("spare", api.NodeReady, 1, 1000))
	got = Plan(in)
	if len(got.Stop) != 0 || len(got.Place) != 1 || got.Place[0].Replaces != "old" || len(got.Blocked) != 0 {
		t.Fatalf("rollout did not recover on spare capacity: %+v", got)
	}
}

func TestPlacementExplainsGangCapacityAndFabric(t *testing.T) {
	a := app("train", 2)
	a.Kind, a.Gang, a.GangFabric, a.Resources.CPU = api.KindJob, true, "fabric", 1000
	east, west := node("east", api.NodeReady, 1, 1000), node("west", api.NodeReady, 1, 1000)
	east.Labels, west.Labels = map[string]string{"fabric": "east"}, map[string]string{"fabric": "west"}
	in := Input{Apps: []api.App{a}, Nodes: []api.Node{east, west}}
	got := Plan(in)
	if len(got.Place) != 0 || len(got.Blocked) != 1 || got.Blocked[0].Missing != 2 || !strings.Contains(got.Blocked[0].Reason, "gang fabric label fabric") {
		t.Fatalf("split gang: %+v", got)
	}
	in.Assignments = []api.Assignment{{ID: "old", App: "train", NodeID: "east", Generation: 1, Status: api.StatusRunning, Resources: a.Resources}}
	got = Plan(in)
	if len(got.Place) != 0 || len(got.Blocked) != 1 || got.Blocked[0].Missing != 1 || !strings.Contains(got.Blocked[0].Nodes[1].Reason, "gang is on fabric=east") {
		t.Fatalf("gang lost its existing fabric: %+v", got)
	}
	in.Nodes[1].Labels = nil
	got = Plan(in)
	if !strings.Contains(got.Blocked[0].Nodes[1].Reason, "missing gang fabric label") {
		t.Fatalf("missing fabric not reported: %+v", got)
	}
}

func TestPlacementDoesNotReportWaitingOrCompletedAssignmentsAsUnplaced(t *testing.T) {
	for _, status := range []string{api.StatusPending, api.StatusStarting, api.StatusRunning, api.StatusFailed, api.StatusSucceeded} {
		t.Run(status, func(t *testing.T) {
			a := app("job", 1)
			a.Kind = api.KindJob
			in := Input{Apps: []api.App{a}, Nodes: []api.Node{node("worker", api.NodeReady, 1, 1000)}, Assignments: []api.Assignment{{ID: "one", App: "job", NodeID: "worker", Generation: 1, Status: status}}}
			got := Plan(in)
			if len(got.Blocked) != 0 || len(got.Place) != 0 {
				t.Fatalf("existing desired or completed work reported unplaced: %+v", got)
			}
		})
	}
	a := app("web", 0)
	if got := Plan(Input{Apps: []api.App{a}}); len(got.Blocked) != 0 {
		t.Fatalf("scaled-down app reported blocked: %+v", got)
	}
	a.Replicas = 1
	if got := Plan(Input{Apps: []api.App{a}}); len(got.Blocked) != 1 || got.Blocked[0].Reason != "no nodes have joined" {
		t.Fatalf("missing nodes not reported: %+v", got)
	}
}
