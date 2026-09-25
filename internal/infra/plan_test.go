package infra

import (
	"strings"
	"testing"

	"tessera/internal/api"
)

func TestPlanCreatesInDependencyOrderAndIsIdempotent(t *testing.T) {
	desired, err := Parse([]byte(`networks:
  - name: front
    provider: aws
    region: us-east-1
    cidr: 10.20.0.0/16
machines:
  - name: worker
    provider: aws
    region: us-east-1
    type: g5.xlarge
    image: ami-agent
    network: front
apps:
  - name: web
    image: nginx:alpine
    resources:
      cpu: 100m
      memory: 128Mi
`))
	if err != nil || len(desired.Apps) != 1 || desired.Apps[0].Resources.Memory != 128<<20 {
		t.Fatalf("parsed %+v: %v", desired, err)
	}
	plan, err := Plan(desired, State{})
	if err != nil || len(plan.Actions) != 3 || plan.Actions[0].Kind != "network" || plan.Actions[1].Kind != "machine" || plan.Actions[2].Kind != "app" {
		t.Fatalf("ordered plan %+v: %v", plan, err)
	}
	state := State{
		Networks: []ExistingNetwork{{Network: desired.Networks[0], Owned: true}},
		Machines: []ExistingMachine{{Machine: desired.Machines[0], Owned: true}},
		Apps:     []ExistingApp{{App: desired.Apps[0], Owned: true}},
	}
	repeat, err := Plan(desired, state)
	if err != nil || repeat.Generation != plan.Generation || len(repeat.Actions) != 0 {
		t.Fatalf("second plan %+v: %v", repeat, err)
	}
}

func TestPlanProposesOwnedRemovalAndNeverDeletesAdopted(t *testing.T) {
	state := State{
		Networks: []ExistingNetwork{{Network: Network{Name: "owned"}, Owned: true}, {Network: Network{Name: "adopted"}, Owned: false}},
		Machines: []ExistingMachine{{Machine: Machine{Name: "worker"}, Owned: true}},
		Apps:     []ExistingApp{{App: app("web"), Owned: true}},
	}
	plan, err := Plan(Desired{}, state)
	if err != nil || len(plan.Actions) != 3 {
		t.Fatalf("removal plan %+v: %v", plan, err)
	}
	for _, action := range plan.Actions {
		if action.Operation != "confirm" || action.Name == "adopted" {
			t.Fatalf("unsafe removal %+v", action)
		}
	}
	if plan.Actions[0].Kind != "app" || plan.Actions[1].Kind != "machine" || plan.Actions[2].Kind != "network" {
		t.Fatalf("unsafe removal order %+v", plan.Actions)
	}
}

func TestPlanFlagsMachineReplacementAndRejectsNetworkMismatch(t *testing.T) {
	desired := Desired{Networks: []Network{{Name: "front", Provider: "aws", Region: "us-east-1", CIDR: "10.0.0.0/24"}}, Machines: []Machine{{Name: "worker", Provider: "aws", Region: "us-east-1", Type: "g5.xlarge", Image: "ami-a", Network: "front"}}}
	state := State{Networks: []ExistingNetwork{{Network: desired.Networks[0], Owned: true}}, Machines: []ExistingMachine{{Machine: Machine{Name: "worker", Provider: "aws", Region: "us-east-1", Type: "m5.large", Image: "ami-a", Network: "front"}, Owned: true}}}
	plan, err := Plan(desired, state)
	if err != nil || len(plan.Actions) != 1 || plan.Actions[0].Operation != "confirm" {
		t.Fatalf("replacement plan %+v: %v", plan, err)
	}
	desired.Machines[0].Region = "us-west-2"
	if _, err := Plan(desired, state); err == nil || !strings.Contains(err.Error(), "incompatible network") {
		t.Fatalf("accepted network mismatch: %v", err)
	}
}

func TestGenerationIgnoresResourceListOrder(t *testing.T) {
	a := Desired{Networks: []Network{{Name: "b", Provider: "aws", Region: "us-east-1", CIDR: "10.1.0.0/24"}, {Name: "a", Provider: "aws", Region: "us-east-1", CIDR: "10.0.0.0/24"}}}
	b := Desired{Networks: []Network{a.Networks[1], a.Networks[0]}}
	first, err := Plan(a, State{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Plan(b, State{})
	if err != nil || first.Generation != second.Generation {
		t.Fatalf("generation changed with list order: %s vs %s: %v", first.Generation, second.Generation, err)
	}
}

func app(name string) api.App {
	return api.App{Kind: api.KindApp, Name: name, Image: "nginx:alpine", Replicas: 1}
}
