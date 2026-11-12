package schedule

import (
	"fmt"
	"sort"

	"tessera/internal/api"
)

func placementBlocked(app api.App, missing int, reason string, nodes []api.Node, active []api.Assignment, used map[string]api.Resources, usedGPUs map[string]int, usedDevices map[string]map[string]bool, usedPorts map[string]map[int]bool) Blocked {
	blocked := Blocked{App: app.Name, Missing: missing, Reason: reason}
	if len(nodes) == 0 {
		blocked.Reason = "no nodes have joined"
		return blocked
	}
	fabric := ""
	if app.GangFabric != "" {
		for _, assignment := range active {
			if node, ok := nodeByID(nodes, assignment.NodeID); ok {
				fabric = node.Labels[app.GangFabric]
				if fabric != "" {
					break
				}
			}
		}
	}
	for _, node := range nodes {
		failure := placementFailure(node, used[node.ID], usedGPUs[node.ID], usedDevices[node.ID], usedPorts[node.ID], app)
		if failure == "" && app.GangFabric != "" {
			value := node.Labels[app.GangFabric]
			if value == "" {
				failure = "missing gang fabric label " + app.GangFabric
			} else if fabric != "" && value != fabric {
				failure = fmt.Sprintf("gang is on %s=%s; node reports %q", app.GangFabric, fabric, value)
			}
		}
		if failure == "" {
			failure = "fits an individual worker; the whole gang does not fit together"
		}
		blocked.Nodes = append(blocked.Nodes, NodeFailure{NodeID: node.ID, Reason: failure})
	}
	sort.Slice(blocked.Nodes, func(i, j int) bool { return blocked.Nodes[i].NodeID < blocked.Nodes[j].NodeID })
	return blocked
}
