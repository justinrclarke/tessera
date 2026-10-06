package schedule

import (
	"testing"
	"time"

	"tessera/internal/api"
)

func node(id, status string, cpu float64, cap int64) api.Node {
	return api.Node{
		ID: id, Status: status,
		Capacity: api.Resources{CPU: cap, Memory: 1 << 30},
		Free:     api.Resources{CPU: cap, Memory: 1 << 30},
		Perf:     api.Perf{CPU: cpu, Memory: cpu, Disk: cpu},
		Score:    cpu,
	}
}

func app(name string, replicas int) api.App {
	return api.App{Kind: api.KindApp, Name: name, Image: "nginx", Replicas: replicas, Generation: 1, Resources: api.Resources{CPU: 100, Memory: 1 << 20}}
}

func TestPlacesDeficit(t *testing.T) {
	in := Input{
		Apps:  []api.App{app("web", 2)},
		Nodes: []api.Node{node("a", api.NodeReady, 1, 4000), node("b", api.NodeReady, 1, 4000)},
		Now:   time.Unix(0, 0),
	}
	got := Plan(in)
	if len(got.Place) != 2 || len(got.Stop) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestRunningIsStable(t *testing.T) {
	a := app("web", 1)
	in := Input{
		Apps:  []api.App{a},
		Nodes: []api.Node{node("a", api.NodeReady, 1, 4000)},
		Assignments: []api.Assignment{{
			ID: "1", App: "web", NodeID: "a", Generation: 1, Status: api.StatusRunning,
		}},
	}
	got := Plan(in)
	if len(got.Place) != 0 || len(got.Stop) != 0 {
		t.Fatalf("stable plan changed: %+v", got)
	}
}

func TestDeadNodeReschedules(t *testing.T) {
	in := Input{
		Apps:  []api.App{app("web", 1)},
		Nodes: []api.Node{node("dead", api.NodeDead, 1, 4000), node("live", api.NodeReady, 1, 4000)},
		Assignments: []api.Assignment{{
			ID: "old", App: "web", NodeID: "dead", Generation: 1, Status: api.StatusRunning,
		}},
	}
	got := Plan(in)
	if len(got.Stop) != 1 || got.Stop[0] != "old" || len(got.Place) != 1 || got.Place[0].NodeID != "live" {
		t.Fatalf("%+v", got)
	}
}

func TestDoesNotFit(t *testing.T) {
	a := app("web", 1)
	a.Resources.CPU = 5000
	in := Input{Apps: []api.App{a}, Nodes: []api.Node{node("a", api.NodeReady, 1, 1000)}}
	if got := Plan(in); len(got.Place) != 0 {
		t.Fatalf("placed without capacity: %+v", got)
	}
}

func TestGPUOnlyOnGPUNode(t *testing.T) {
	a := app("model", 1)
	a.GPUs = 1
	a.SensitiveTo = "gpu"
	cpu := node("cpu", api.NodeReady, 2, 4000)
	gpu := node("gpu", api.NodeReady, 1, 4000)
	gpu.GPUs = 1
	in := Input{Apps: []api.App{a}, Nodes: []api.Node{cpu, gpu}}
	got := Plan(in)
	if len(got.Place) != 1 || got.Place[0].NodeID != "gpu" {
		t.Fatalf("%+v", got)
	}
}

func TestGPUModelMemoryAndCapacity(t *testing.T) {
	a := app("model", 2)
	a.GPUs = 1
	a.GPUModel = "NVIDIA H100"
	a.GPUMemory = 12 << 30
	a.SensitiveTo = "gpu"
	n := node("gpu", api.NodeReady, 1, 4000)
	n.GPUs = 2
	n.GPUInventory = []api.GPU{
		{UUID: "one", Model: "NVIDIA H100", MemoryFree: 20 << 30},
		{UUID: "two", Model: "NVIDIA A100", MemoryFree: 40 << 30},
	}
	got := Plan(Input{Apps: []api.App{a}, Nodes: []api.Node{n}})
	if len(got.Place) != 1 || got.Place[0].NodeID != "gpu" {
		t.Fatalf("placed outside matching GPU capacity: %+v", got)
	}
	n.GPUInventory[0].MemoryFree = 8 << 30
	if got := Plan(Input{Apps: []api.App{a}, Nodes: []api.Node{n}}); len(got.Place) != 0 {
		t.Fatalf("placed without GPU memory: %+v", got)
	}
}

func TestGPUAssignmentConsumesCapacity(t *testing.T) {
	a := app("model", 2)
	a.GPUs = 1
	n := node("gpu", api.NodeReady, 1, 4000)
	n.GPUs = 1
	got := Plan(Input{Apps: []api.App{a}, Nodes: []api.Node{n}, Assignments: []api.Assignment{{ID: "one", App: "model", NodeID: "gpu", Generation: 1, Status: api.StatusRunning, GPUs: 1}}})
	if len(got.Place) != 0 {
		t.Fatalf("overcommitted GPU: %+v", got)
	}
}

func TestGPUReplicasReserveDistinctDevices(t *testing.T) {
	a := app("model", 2)
	a.GPUs = 1
	n := node("gpu", api.NodeReady, 1, 4000)
	n.GPUs = 2
	n.GPUInventory = []api.GPU{{UUID: "gpu-b", MemoryFree: 16 << 30}, {UUID: "gpu-a", MemoryFree: 16 << 30}}
	got := Plan(Input{Apps: []api.App{a}, Nodes: []api.Node{n}})
	if len(got.Place) != 2 || len(got.Place[0].GPUDevices) != 1 || len(got.Place[1].GPUDevices) != 1 {
		t.Fatalf("missing device reservations: %+v", got)
	}
	if got.Place[0].GPUDevices[0] != "gpu-a" || got.Place[1].GPUDevices[0] != "gpu-b" {
		t.Fatalf("devices were reused or unstable: %+v", got.Place)
	}
}

func TestMoveOnlyAboveGain(t *testing.T) {
	a := app("web", 1)
	a.SensitiveTo = "cpu"
	slow := node("slow", api.NodeReady, 1, 4000)
	fast := node("fast", api.NodeReady, 1.1, 4000)
	in := Input{
		Apps: []api.App{a}, Nodes: []api.Node{slow, fast}, AllowMove: true, MinGain: 0.15,
		Assignments: []api.Assignment{{ID: "old", App: "web", NodeID: "slow", Generation: 1, Status: api.StatusRunning}},
		Now:         time.Unix(100, 0),
	}
	if got := Plan(in); len(got.Place) != 0 {
		t.Fatalf("small gain moved: %+v", got)
	}
	fast.Perf.CPU = 2
	in.Nodes = []api.Node{slow, fast}
	got := Plan(in)
	if len(got.Place) != 1 || got.Place[0].NodeID != "fast" || got.Place[0].Replaces != "old" {
		t.Fatalf("want replacement: %+v", got)
	}
	if len(got.Stop) != 0 {
		t.Fatalf("must not stop before replacement runs: %+v", got)
	}
}

func TestMoveCompletesWhenReplacementRuns(t *testing.T) {
	a := app("web", 1)
	in := Input{
		Apps:  []api.App{a},
		Nodes: []api.Node{node("slow", api.NodeReady, 1, 4000), node("fast", api.NodeReady, 2, 4000)},
		Assignments: []api.Assignment{
			{ID: "old", App: "web", NodeID: "slow", Generation: 1, Status: api.StatusRunning},
			{ID: "new", App: "web", NodeID: "fast", Generation: 1, Status: api.StatusRunning, Replaces: "old"},
		},
		AllowMove: true,
	}
	got := Plan(in)
	if len(got.Stop) != 1 || got.Stop[0] != "old" {
		t.Fatalf("%+v", got)
	}
}

func TestCooldownBlocksMove(t *testing.T) {
	a := app("web", 1)
	now := time.Unix(100, 0)
	in := Input{
		Apps: []api.App{a}, AllowMove: true, MinGain: 0.15, Cooldown: time.Minute, Now: now,
		LastMove:    map[string]time.Time{"web": now.Add(-time.Second)},
		Nodes:       []api.Node{node("slow", api.NodeReady, 1, 4000), node("fast", api.NodeReady, 5, 4000)},
		Assignments: []api.Assignment{{ID: "old", App: "web", NodeID: "slow", Generation: 1, Status: api.StatusRunning}},
	}
	if got := Plan(in); len(got.Place) != 0 {
		t.Fatalf("cooldown ignored: %+v", got)
	}
}

func TestGangAllOrNothing(t *testing.T) {
	a := app("train", 2)
	a.Kind = api.KindJob
	a.Gang = true
	a.Resources.CPU = 3000
	in := Input{Apps: []api.App{a}, Nodes: []api.Node{node("a", api.NodeReady, 1, 4000)}}
	if got := Plan(in); len(got.Place) != 0 {
		t.Fatalf("partial gang: %+v", got)
	}
	in.Nodes = append(in.Nodes, node("b", api.NodeReady, 1, 4000))
	got := Plan(in)
	if len(got.Place) != 2 {
		t.Fatalf("gang: %+v", got)
	}
}

func TestGangUsesOneFabricAndNodeLabels(t *testing.T) {
	a := app("train", 2)
	a.Kind = api.KindJob
	a.Gang = true
	a.GangFabric = "fabric"
	a.NodeLabels = map[string]string{"storage": "shared"}
	a.Resources.CPU = 3000
	aNode := node("a", api.NodeReady, 1, 4000)
	bNode := node("b", api.NodeReady, 2, 4000)
	cNode := node("c", api.NodeReady, 3, 4000)
	aNode.Labels = map[string]string{"fabric": "east", "storage": "shared"}
	bNode.Labels = map[string]string{"fabric": "west", "storage": "shared"}
	cNode.Labels = map[string]string{"fabric": "east", "storage": "other"}
	in := Input{Apps: []api.App{a}, Nodes: []api.Node{aNode, bNode, cNode}}
	if got := Plan(in); len(got.Place) != 0 {
		t.Fatalf("split or mismatched gang: %+v", got)
	}
	cNode.Labels["storage"] = "shared"
	in.Nodes[2] = cNode
	got := Plan(in)
	if len(got.Place) != 2 || got.Place[0].NodeID != "c" || got.Place[1].NodeID != "a" {
		t.Fatalf("gang did not stay on east fabric: %+v", got)
	}
}

func TestRunningJobDoesNotMoveWithoutCheckpoint(t *testing.T) {
	a := app("train", 1)
	a.Kind = api.KindJob
	a.SensitiveTo = "cpu"
	in := Input{
		Apps: []api.App{a}, Nodes: []api.Node{node("slow", api.NodeReady, 1, 4000), node("fast", api.NodeReady, 5, 4000)},
		Assignments: []api.Assignment{{ID: "worker", App: "train", NodeID: "slow", Generation: 1, Status: api.StatusRunning}},
		AllowMove:   true,
	}
	if got := Plan(in); len(got.Place) != 0 || len(got.Stop) != 0 {
		t.Fatalf("moved job without checkpoint: %+v", got)
	}
}

func TestDependencyHold(t *testing.T) {
	web := app("web", 1)
	web.DependsOn = []string{"db"}
	db := app("db", 1)
	in := Input{Apps: []api.App{db, web}, Nodes: []api.Node{node("a", api.NodeReady, 1, 8000)}}
	got := Plan(in)
	if len(got.Place) != 1 || got.Place[0].App != "db" {
		t.Fatalf("dependent placed early: %+v", got)
	}
}

func TestFailedOverMaxRestartsReplaced(t *testing.T) {
	in := Input{
		Apps:        []api.App{app("web", 1)},
		Nodes:       []api.Node{node("a", api.NodeReady, 1, 4000), node("b", api.NodeReady, 1, 4000)},
		MaxRestarts: 3,
		Assignments: []api.Assignment{{ID: "bad", App: "web", NodeID: "a", Generation: 1, Status: api.StatusFailed, Restarts: 3}},
	}
	got := Plan(in)
	if len(got.Stop) != 1 || len(got.Place) != 1 {
		t.Fatalf("%+v", got)
	}
}
