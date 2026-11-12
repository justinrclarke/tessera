package proxy

import (
	"reflect"
	"testing"

	"tessera/internal/api"
)

func TestRouteSelectsItsMappedTargetPort(t *testing.T) {
	assignments := []api.Assignment{{App: "web", NodeID: "node", Status: api.StatusRunning, HostPort: 31000, HostPorts: map[int]int{80: 31000, 8080: 31001}}}
	nodes := []api.Node{{ID: "node", Addr: "10.0.0.1", Status: api.NodeReady}}
	for _, tc := range []struct{ target, port int }{{80, 31000}, {8080, 31001}} {
		route := api.Route{App: "web", TargetPort: tc.target}
		want := "10.0.0.1:31000"
		if tc.port == 31001 {
			want = "10.0.0.1:31001"
		}
		if got := Backends(route, assignments, nodes); !reflect.DeepEqual(got, []string{want}) {
			t.Fatalf("target %d routed to %v", tc.target, got)
		}
	}
	if got := Backends(api.Route{App: "web", TargetPort: 9999}, assignments, nodes); len(got) != 0 {
		t.Fatalf("unpublished target fell back to another port: %v", got)
	}
}
