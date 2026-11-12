package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
)

func TestPersistentStorageWaitsForAcknowledgementAndRejectsSecondWriter(t *testing.T) {
	s, c := standaloneClient(t)
	ctx := context.Background()
	now := time.Now()
	s.Now = func() time.Time { return now }
	for _, id := range []string{"owner", "other"} {
		if _, err := c.Register(ctx, client.RegisterRequest{ID: id, StorageCapable: true}); err != nil {
			t.Fatal(err)
		}
	}
	manifest := func(name, image string, replicas int) []byte {
		return []byte(fmt.Sprintf("kind: App\nname: %s\nimage: %s\nreplicas: %d\nstorage: {node: owner, source: /srv/data, target: /data}\n", name, image, replicas))
	}
	if err := c.Apply(ctx, manifest("db", "old", 1)); err != nil {
		t.Fatal(err)
	}
	asgs, _ := s.Store.ListAssignments()
	if len(asgs) != 1 || asgs[0].NodeID != "owner" || asgs[0].Env["TESSERA_WORKLOAD_ID"] != "db" {
		t.Fatalf("%+v", asgs)
	}
	id := asgs[0].ID
	if err := c.Report(ctx, id, client.StatusReport{Status: api.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(ctx, manifest("second", "old", 1)); err == nil {
		t.Fatal("second writer accepted")
	}
	if err := c.Apply(ctx, manifest("db", "new", 1)); err == nil {
		t.Fatal("online release replacement accepted")
	}
	now = now.Add(time.Minute)
	if err := s.Reconcile(now); err != nil {
		t.Fatal(err)
	}
	asgs, _ = s.Store.ListAssignments()
	if len(asgs) != 1 || asgs[0].Status != api.StatusRunning {
		t.Fatalf("node loss replaced writer: %+v", asgs)
	}
	if err := c.Apply(ctx, manifest("db", "old", 0)); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(ctx, manifest("db", "new", 1)); err == nil {
		t.Fatal("changed release before stop acknowledgement")
	}
	stopping, _ := s.Store.GetAssignment(id)
	if stopping.Status != api.StatusStopping {
		t.Fatalf("%+v", stopping)
	}
	if err := c.Report(ctx, id, client.StatusReport{Status: api.StatusRunning}); err == nil {
		t.Fatal("late report canceled stop")
	}
	if err := c.Report(ctx, id, client.StatusReport{Status: api.StatusStopped}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register(ctx, client.RegisterRequest{ID: "owner", StorageCapable: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(ctx, manifest("db", "new", 1)); err != nil {
		t.Fatal(err)
	}
	asgs, _ = s.Store.ListAssignments()
	active := 0
	for _, a := range asgs {
		if api.Active(a.Status) {
			active++
			if a.NodeID != "owner" || a.Image != "new" {
				t.Fatalf("%+v", a)
			}
		}
	}
	if active != 1 {
		t.Fatalf("%d active writers", active)
	}
}

func TestPersistentConfigUpdateIsAtomic(t *testing.T) {
	s, c := standaloneClient(t)
	ctx := context.Background()
	c.Register(ctx, client.RegisterRequest{ID: "owner", StorageCapable: true})
	manifest := "kind: Config\nname: settings\ndata: {value: old}\n---\nkind: App\nname: db\nimage: db\nconfigs: [settings]\nstorage: {node: owner, source: /srv/db, target: /data}\n"
	if err := c.Apply(ctx, []byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(ctx, []byte("kind: Config\nname: settings\ndata: {value: new}\n")); err == nil || !strings.Contains(err.Error(), "scale to zero") {
		t.Fatalf("%v", err)
	}
	config, _ := s.Store.GetConfig("settings")
	if config.Data["value"] != "old" {
		t.Fatal("failed update changed config")
	}
}
