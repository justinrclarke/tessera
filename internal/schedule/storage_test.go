package schedule

import (
	"tessera/internal/api"
	"testing"
)

func TestStoragePinsPlacementAndRequiresCapableAgent(t *testing.T) {
	app := api.App{Name: "db", Kind: api.KindApp, Replicas: 1, Storage: &api.Storage{Node: "owner", Source: "/srv/db", Target: "/data"}}
	input := Input{Apps: []api.App{app}, Nodes: []api.Node{{ID: "other", Status: api.NodeReady, StorageCapable: true}, {ID: "owner", Status: api.NodeReady}}}
	if plan := Plan(input); len(plan.Place) != 0 {
		t.Fatalf("placed on old agent or wrong node: %+v", plan)
	}
	input.Nodes[1].StorageCapable = true
	if plan := Plan(input); len(plan.Place) != 1 || plan.Place[0].NodeID != "owner" {
		t.Fatalf("%+v", plan)
	}
	input.Assignments = []api.Assignment{{ID: "writer", App: "db", NodeID: "owner", Storage: app.Storage, Status: api.StatusStopping}}
	if plan := Plan(input); len(plan.Place) != 0 {
		t.Fatalf("replaced writer before stop acknowledgement: %+v", plan)
	}
}
