package schedule

import (
	"sort"
	"time"

	"tessera/internal/api"
)

type Placement struct {
	App        string
	NodeID     string
	Generation int64
	Replaces   string
	GPUDevices []string
}

type Input struct {
	Apps        []api.App
	Nodes       []api.Node
	Assignments []api.Assignment
	MinGain     float64
	Cooldown    time.Duration
	MaxRestarts int
	LastMove    map[string]time.Time
	Now         time.Time
	AllowMove   bool
}

type Result struct {
	Place []Placement
	Stop  []string
}

func Plan(in Input) Result {
	if in.MaxRestarts == 0 {
		in.MaxRestarts = 3
	}
	if in.MinGain == 0 {
		in.MinGain = 0.15
	}
	nodes := map[string]api.Node{}
	for _, n := range in.Nodes {
		nodes[n.ID] = n
	}
	apps := map[string]api.App{}
	for _, a := range in.Apps {
		apps[a.Name] = a
	}
	runningApps := map[string]int{}
	for _, asg := range in.Assignments {
		if asg.Status == api.StatusRunning {
			runningApps[asg.App]++
		}
	}

	stop := map[string]bool{}
	for _, asg := range in.Assignments {
		if !api.Active(asg.Status) {
			continue
		}
		app, ok := apps[asg.App]
		if !ok {
			stop[asg.ID] = true
			continue
		}
		n, has := nodes[asg.NodeID]
		if !has || n.Status == api.NodeDead {
			stop[asg.ID] = true
			continue
		}
		if asg.Generation != app.Generation {
			stop[asg.ID] = true
			continue
		}
		if asg.Status == api.StatusFailed && asg.Restarts >= in.MaxRestarts {
			stop[asg.ID] = true
			continue
		}
		if !depsReady(app, runningApps) {
			stop[asg.ID] = true
		}
	}
	for _, asg := range in.Assignments {
		if asg.Replaces != "" && asg.Status == api.StatusRunning && api.Active(asg.Status) && !stop[asg.ID] {
			if _, ok := find(in.Assignments, asg.Replaces); ok {
				stop[asg.Replaces] = true
			}
		}
	}

	var res Result
	for id := range stop {
		res.Stop = append(res.Stop, id)
	}
	sort.Strings(res.Stop)

	used := map[string]api.Resources{}
	usedGPUs := map[string]int{}
	usedDevices := map[string]map[string]bool{}
	load := map[string]int{}
	active := map[string][]api.Assignment{}
	for _, asg := range in.Assignments {
		if stop[asg.ID] || !api.Active(asg.Status) {
			continue
		}
		app := apps[asg.App]
		used[asg.NodeID] = add(used[asg.NodeID], reqOf(app, asg))
		gpuCount := asg.GPUs
		if gpuCount == 0 {
			gpuCount = app.GPUs
		}
		usedGPUs[asg.NodeID] += gpuCount
		if len(asg.GPUDevices) > 0 {
			if usedDevices[asg.NodeID] == nil {
				usedDevices[asg.NodeID] = map[string]bool{}
			}
			for _, id := range asg.GPUDevices {
				usedDevices[asg.NodeID][id] = true
			}
		}
		load[asg.NodeID]++
		active[asg.App] = append(active[asg.App], asg)
	}

	names := make([]string, 0, len(in.Apps))
	for _, a := range in.Apps {
		names = append(names, a.Name)
	}
	sort.Strings(names)

	ready := readyNodes(in.Nodes)
	for _, name := range names {
		app := apps[name]
		if !depsReady(app, runningApps) {
			continue
		}
		cur := active[name]
		extra := 0
		for _, asg := range cur {
			if asg.Replaces != "" {
				extra++
			}
		}
		count := len(cur) - extra
		if app.Kind == api.KindJob {
			done := 0
			for _, asg := range in.Assignments {
				if asg.App == name && asg.Status == api.StatusSucceeded && asg.Generation == app.Generation {
					done++
				}
			}
			if done >= app.Replicas {
				continue
			}
		}
		if count < app.Replicas {
			need := app.Replicas - count
			if app.Gang {
				placed, ok := gang(app, ready, cur, used, usedGPUs, usedDevices, load, need)
				if ok {
					res.Place = append(res.Place, placed...)
					for _, p := range placed {
						used[p.NodeID] = add(used[p.NodeID], app.Resources)
						usedGPUs[p.NodeID] += app.GPUs
						reserveDevices(usedDevices, p.NodeID, p.GPUDevices)
						load[p.NodeID]++
					}
				}
			} else {
				for i := 0; i < need; i++ {
					n, ok := pick(ready, used, usedGPUs, usedDevices, load, app)
					if !ok {
						break
					}
					ids, _ := selectGPUDevices(n, app, usedGPUs[n.ID], usedDevices[n.ID])
					res.Place = append(res.Place, Placement{App: app.Name, NodeID: n.ID, Generation: app.Generation, GPUDevices: ids})
					used[n.ID] = add(used[n.ID], app.Resources)
					usedGPUs[n.ID] += app.GPUs
					reserveDevices(usedDevices, n.ID, ids)
					load[n.ID]++
				}
			}
		}
		if count > app.Replicas {
			surplus := count - app.Replicas
			cands := make([]api.Assignment, 0, len(cur))
			for _, asg := range cur {
				if asg.Replaces != "" {
					continue
				}
				replaced := false
				for _, o := range cur {
					if o.Replaces == asg.ID {
						replaced = true
					}
				}
				if replaced {
					continue
				}
				cands = append(cands, asg)
			}
			sort.Slice(cands, func(i, j int) bool {
				if cands[i].Status == api.StatusPending && cands[j].Status != api.StatusPending {
					return true
				}
				si := nodes[cands[i].NodeID].Perf.CPU
				sj := nodes[cands[j].NodeID].Perf.CPU
				if si != sj {
					return si < sj
				}
				return cands[i].ID < cands[j].ID
			})
			for i := 0; i < surplus && i < len(cands); i++ {
				res.Stop = append(res.Stop, cands[i].ID)
			}
		}
		if in.AllowMove && count == app.Replicas {
			if mv, ok := move(app, cur, ready, used, usedGPUs, usedDevices, load, in); ok {
				res.Place = append(res.Place, mv)
				used[mv.NodeID] = add(used[mv.NodeID], app.Resources)
				usedGPUs[mv.NodeID] += app.GPUs
				reserveDevices(usedDevices, mv.NodeID, mv.GPUDevices)
				load[mv.NodeID]++
			}
		}
	}
	sort.Strings(res.Stop)
	return res
}

func move(app api.App, cur []api.Assignment, ready []api.Node, used map[string]api.Resources, usedGPUs map[string]int, usedDevices map[string]map[string]bool, load map[string]int, in Input) (Placement, bool) {
	if app.Kind == api.KindJob {
		return Placement{}, false
	}
	if !in.LastMove[app.Name].IsZero() && in.Now.Sub(in.LastMove[app.Name]) < in.Cooldown {
		return Placement{}, false
	}
	var oldest api.Assignment
	found := false
	for _, asg := range cur {
		if asg.Status != api.StatusRunning || asg.Replaces != "" {
			continue
		}
		for _, o := range cur {
			if o.Replaces == asg.ID {
				return Placement{}, false
			}
		}
		if !found || asg.ID < oldest.ID {
			oldest = asg
			found = true
		}
	}
	if !found {
		return Placement{}, false
	}
	curNode, ok := nodeByID(ready, oldest.NodeID)
	if !ok {
		var all []api.Node
		all = append(all, ready...)
		curNode, ok = nodeByID(in.Nodes, oldest.NodeID)
		if !ok {
			return Placement{}, false
		}
		_ = all
	}
	best, ok := pick(ready, used, usedGPUs, usedDevices, load, app)
	if !ok || best.ID == oldest.NodeID {
		return Placement{}, false
	}
	gain := Gain(best.Perf, curNode.Perf, app.SensitiveTo)
	if app.SensitiveTo == "gpu" {
		current := rank(curNode, app)
		if current == 0 {
			return Placement{}, false
		}
		gain = rank(best, app)/current - 1
	}
	if gain < in.MinGain {
		return Placement{}, false
	}
	ids, _ := selectGPUDevices(best, app, usedGPUs[best.ID], usedDevices[best.ID])
	return Placement{App: app.Name, NodeID: best.ID, Generation: app.Generation, Replaces: oldest.ID, GPUDevices: ids}, true
}

func Gain(candidate, current api.Perf, sensitive string) float64 {
	var c, n float64
	switch sensitive {
	case "memory":
		c, n = current.Memory, candidate.Memory
	case "disk":
		c, n = current.Disk, candidate.Disk
	default:
		c, n = current.CPU, candidate.CPU
		if c == 0 && n == 0 {
			return 0
		}
	}
	if c == 0 {
		return 0
	}
	return n/c - 1
}

func gang(app api.App, ready []api.Node, active []api.Assignment, used map[string]api.Resources, usedGPUs map[string]int, usedDevices map[string]map[string]bool, load map[string]int, need int) ([]Placement, bool) {
	if app.GangFabric == "" {
		return gangOnNodes(app, ready, used, usedGPUs, usedDevices, load, need)
	}
	groups := map[string][]api.Node{}
	byID := map[string]api.Node{}
	for _, n := range ready {
		byID[n.ID] = n
		if fabric := n.Labels[app.GangFabric]; fabric != "" {
			groups[fabric] = append(groups[fabric], n)
		}
	}
	required := ""
	for _, asg := range active {
		n, ok := byID[asg.NodeID]
		if !ok || n.Labels[app.GangFabric] == "" {
			return nil, false
		}
		if required != "" && n.Labels[app.GangFabric] != required {
			return nil, false
		}
		required = n.Labels[app.GangFabric]
	}
	var fabrics []string
	for fabric := range groups {
		if required == "" || fabric == required {
			fabrics = append(fabrics, fabric)
		}
	}
	sort.Strings(fabrics)
	for _, fabric := range fabrics {
		if placed, ok := gangOnNodes(app, groups[fabric], used, usedGPUs, usedDevices, load, need); ok {
			return placed, true
		}
	}
	return nil, false
}

func gangOnNodes(app api.App, ready []api.Node, used map[string]api.Resources, usedGPUs map[string]int, usedDevices map[string]map[string]bool, load map[string]int, need int) ([]Placement, bool) {
	u := map[string]api.Resources{}
	g := map[string]int{}
	idsUsed := cloneDevices(usedDevices)
	l := map[string]int{}
	for k, v := range used {
		u[k] = v
	}
	for k, v := range usedGPUs {
		g[k] = v
	}
	for k, v := range load {
		l[k] = v
	}
	var out []Placement
	for i := 0; i < need; i++ {
		n, ok := pick(ready, u, g, idsUsed, l, app)
		if !ok {
			return nil, false
		}
		ids, _ := selectGPUDevices(n, app, g[n.ID], idsUsed[n.ID])
		out = append(out, Placement{App: app.Name, NodeID: n.ID, Generation: app.Generation, GPUDevices: ids})
		u[n.ID] = add(u[n.ID], app.Resources)
		g[n.ID] += app.GPUs
		reserveDevices(idsUsed, n.ID, ids)
		l[n.ID]++
	}
	return out, true
}

func pick(nodes []api.Node, used map[string]api.Resources, usedGPUs map[string]int, usedDevices map[string]map[string]bool, load map[string]int, app api.App) (api.Node, bool) {
	var best api.Node
	found := false
	for _, n := range nodes {
		if n.Status != api.NodeReady {
			continue
		}
		if !fits(n, used[n.ID], usedGPUs[n.ID], usedDevices[n.ID], app) {
			continue
		}
		if !found || better(n, load[n.ID], best, load[best.ID], app) {
			best = n
			found = true
		}
	}
	return best, found
}

func better(a api.Node, aload int, b api.Node, bload int, app api.App) bool {
	as := rank(a, app)
	bs := rank(b, app)
	if as != bs {
		return as > bs
	}
	if aload != bload {
		return aload < bload
	}
	return a.ID < b.ID
}

func rank(n api.Node, app api.App) float64 {
	switch app.SensitiveTo {
	case "gpu":
		var free int64
		for _, gpu := range n.GPUInventory {
			if app.GPUModel == "" || gpu.Model == app.GPUModel {
				free += gpu.MemoryFree
			}
		}
		if free > 0 {
			return float64(free)
		}
		return float64(n.GPUs)
	case "memory":
		if n.Perf.Memory != 0 {
			return n.Perf.Memory
		}
	case "disk":
		if n.Perf.Disk != 0 {
			return n.Perf.Disk
		}
	}
	if n.Perf.CPU != 0 {
		return n.Perf.CPU
	}
	return n.Score
}

func fits(n api.Node, used api.Resources, usedGPUs int, usedDevices map[string]bool, app api.App) bool {
	for key, value := range app.NodeLabels {
		if n.Labels[key] != value {
			return false
		}
	}
	need := app.Resources
	if need.CPU > 0 && n.Capacity.CPU > 0 && used.CPU+need.CPU > n.Capacity.CPU {
		return false
	}
	if need.Memory > 0 && n.Capacity.Memory > 0 && used.Memory+need.Memory > n.Capacity.Memory {
		return false
	}
	if app.GPUs > 0 && usedGPUs+app.GPUs > n.GPUs {
		return false
	}
	if app.GPUs > 0 && (len(n.GPUInventory) > 0 || app.GPUModel != "" || app.GPUMemory > 0) {
		if _, ok := selectGPUDevices(n, app, usedGPUs, usedDevices); !ok {
			return false
		}
	}
	if need.Memory > 0 && n.Free.Memory > 0 && need.Memory > n.Free.Memory {
		return false
	}
	return true
}

func selectGPUDevices(n api.Node, app api.App, usedCount int, reserved map[string]bool) ([]string, bool) {
	if app.GPUs == 0 {
		return nil, true
	}
	if len(n.GPUInventory) == 0 {
		return nil, app.GPUModel == "" && app.GPUMemory == 0
	}
	var available []string
	for _, gpu := range n.GPUInventory {
		if gpu.UUID == "" || reserved[gpu.UUID] {
			continue
		}
		if app.GPUModel != "" && gpu.Model != app.GPUModel {
			continue
		}
		if app.GPUMemory > 0 && gpu.MemoryFree < app.GPUMemory {
			continue
		}
		available = append(available, gpu.UUID)
	}
	sort.Strings(available)
	legacy := usedCount - len(reserved)
	if legacy < 0 {
		legacy = 0
	}
	if legacy+app.GPUs > len(available) {
		return nil, false
	}
	return available[legacy : legacy+app.GPUs], true
}

func reserveDevices(used map[string]map[string]bool, node string, ids []string) {
	if len(ids) == 0 {
		return
	}
	if used[node] == nil {
		used[node] = map[string]bool{}
	}
	for _, id := range ids {
		used[node][id] = true
	}
}

func cloneDevices(in map[string]map[string]bool) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for node, ids := range in {
		out[node] = map[string]bool{}
		for id := range ids {
			out[node][id] = true
		}
	}
	return out
}

func readyNodes(nodes []api.Node) []api.Node {
	var out []api.Node
	for _, n := range nodes {
		if n.Status == api.NodeReady {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func depsReady(app api.App, running map[string]int) bool {
	for _, d := range app.DependsOn {
		if running[d] == 0 {
			return false
		}
	}
	return true
}

func add(a, b api.Resources) api.Resources {
	return api.Resources{CPU: a.CPU + b.CPU, Memory: a.Memory + b.Memory}
}

func reqOf(app api.App, asg api.Assignment) api.Resources {
	if asg.Resources.CPU != 0 || asg.Resources.Memory != 0 {
		return asg.Resources
	}
	return app.Resources
}

func find(asgs []api.Assignment, id string) (api.Assignment, bool) {
	for _, a := range asgs {
		if a.ID == id {
			return a, true
		}
	}
	return api.Assignment{}, false
}

func nodeByID(nodes []api.Node, id string) (api.Node, bool) {
	for _, n := range nodes {
		if n.ID == id {
			return n, true
		}
	}
	return api.Node{}, false
}
