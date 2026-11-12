package schedule

import (
	"testing"

	"tessera/internal/api"
)

func TestContainerdReservesHostNetworkPortsBeforePlacement(t *testing.T) {
	a := app("web", 2)
	a.Ports = []api.Port{{Container: 80}, {Container: 8080}}
	n := node("host", api.NodeReady, 1, 4000)
	n.Runtime = "ctr"
	got := Plan(Input{Apps: []api.App{a}, Nodes: []api.Node{n}})
	if len(got.Place) != 1 || len(got.Blocked) != 1 {
		t.Fatalf("host-network replicas reused a port: %+v", got)
	}
	a.Ports[0].Host = 8000
	got = Plan(Input{Apps: []api.App{a}, Nodes: []api.Node{n}})
	if len(got.Place) != 0 || len(got.Blocked) != 1 {
		t.Fatalf("unsupported containerd port remapping was placed: %+v", got)
	}
	n.Runtime = "docker"
	got = Plan(Input{Apps: []api.App{a}, Nodes: []api.Node{n}})
	if len(got.Place) != 1 {
		t.Fatalf("Docker fixed host-port reservation failed: %+v", got)
	}
	a.Ports[0].Host = 0
	got = Plan(Input{Apps: []api.App{a}, Nodes: []api.Node{n}})
	if len(got.Place) != 2 {
		t.Fatalf("Docker dynamic port replicas could not share a node: %+v", got)
	}
}
