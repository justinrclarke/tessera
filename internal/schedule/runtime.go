package schedule

import "tessera/internal/api"

func publishedPorts(node api.Node, ports []api.Port) []api.Port {
	if node.Runtime != "ctr" {
		return ports
	}
	out := append([]api.Port(nil), ports...)
	for i := range out {
		out[i].Host = out[i].Container
	}
	return out
}
