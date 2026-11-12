package schedule

import (
	"strconv"
	"tessera/internal/api"
)

func CheckpointTargets(in Input, app api.App) []Placement {
	if app.Kind != api.KindJob || !app.Gang || app.Checkpoint == nil || app.Replicas < 1 {
		return nil
	}
	used, gpu, devices, ports, load := map[string]api.Resources{}, map[string]int{}, map[string]map[string]bool{}, map[string]map[int]bool{}, map[string]int{}
	apps := map[string]api.App{}
	for _, a := range in.Apps {
		apps[a.Name] = a
	}
	oldNodes := map[string]bool{}
	count, score := 0, float64(0)
	ranks := map[int]bool{}
	for _, a := range in.Assignments {
		if !api.Active(a.Status) {
			continue
		}
		used[a.NodeID] = add(used[a.NodeID], reqOf(apps[a.App], a))
		usedGPUs := a.GPUs
		if usedGPUs == 0 {
			usedGPUs = apps[a.App].GPUs
		}
		gpu[a.NodeID] += usedGPUs
		reserveDevices(devices, a.NodeID, a.GPUDevices)
		node, _ := nodeByID(in.Nodes, a.NodeID)
		reservePorts(ports, a.NodeID, publishedPorts(node, a.Ports))
		load[a.NodeID]++
		if a.App == app.Name {
			workerRank, err := strconv.Atoi(a.Env["TESSERA_WORKER_RANK"])
			if err != nil || workerRank < 0 || workerRank >= app.Replicas || ranks[workerRank] {
				return nil
			}
			ranks[workerRank] = true
			n, ok := nodeByID(in.Nodes, a.NodeID)
			if !ok || n.Status != api.NodeReady || a.Status != api.StatusRunning || a.Generation != app.Generation {
				return nil
			}
			oldNodes[a.NodeID] = true
			count++
			score += rank(n, app)
		}
	}
	if count != app.Replicas || score <= 0 {
		return nil
	}
	gain := in.MinGain
	if gain == 0 {
		gain = .15
	}
	var candidates []api.Node
	for _, n := range readyNodes(in.Nodes) {
		if !oldNodes[n.ID] && rank(n, app) >= score/float64(count)*(1+gain) {
			candidates = append(candidates, n)
		}
	}
	placed, ok := gang(app, candidates, nil, used, gpu, devices, ports, load, app.Replicas)
	if !ok {
		return nil
	}
	return placed
}
