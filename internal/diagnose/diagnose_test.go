package diagnose

import (
	"testing"
	"time"

	"tessera/internal/api"
)

func TestBadReleaseRollsBack(t *testing.T) {
	app := api.App{Name: "web", Generation: 2, HealthyGeneration: 1}
	asg := api.Assignment{App: "web", NodeID: "a", Status: api.StatusFailed, Reason: "crash", Restarts: 1}
	got := Scan([]api.App{app}, nil, []api.Assignment{asg}, 3, time.Now())
	if len(got) != 1 || got[0].Action != "rollback" || got[0].Class != "bad_release" {
		t.Fatalf("%+v", got)
	}
}

func TestOOMReschedules(t *testing.T) {
	app := api.App{Name: "web", Generation: 1, HealthyGeneration: 1}
	asg := api.Assignment{App: "web", NodeID: "a", Status: api.StatusFailed, Reason: "oom", Restarts: 3}
	got := Scan([]api.App{app}, nil, []api.Assignment{asg}, 3, time.Now())
	if got[0].Class != "oom" || got[0].Action != "reschedule" {
		t.Fatalf("%+v", got)
	}
}

func TestDiskAndCert(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	n := api.Node{
		ID: "a", Status: api.NodeReady,
		DiskTotal: 100, DiskFree: 5,
		CertNotBefore: now.Add(-2 * time.Hour),
		CertNotAfter:  now.Add(30 * time.Minute),
	}
	got := Scan(nil, []api.Node{n}, nil, 3, now)
	classes := map[string]bool{}
	for _, f := range got {
		classes[f.Class] = true
	}
	if !classes["disk_full"] || !classes["cert_expiry"] {
		t.Fatalf("%+v", got)
	}
}

func TestDependency(t *testing.T) {
	apps := []api.App{{Name: "web", DependsOn: []string{"db"}}, {Name: "db"}}
	got := Scan(apps, nil, nil, 3, time.Now())
	if len(got) != 1 || got[0].Action != "hold" {
		t.Fatalf("%+v", got)
	}
}
