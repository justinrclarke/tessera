package drill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tessera/internal/agent"
	"tessera/internal/api"
	"tessera/internal/runtime"
)

func (r *ownedDocker) Kind() string { return "docker" }

func TestDockerPersistentDataSurvivesStoppedRelease(t *testing.T) {
	if os.Getenv("TESSERA_DOCKER_SMOKE") == "" {
		t.Skip("set TESSERA_DOCKER_SMOKE=1 for Docker storage acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	_, c, cleanup := bootWithClock(time.Now)
	defer cleanup()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rtm := &ownedDocker{Docker: runtime.NewDocker(runtime.DockerSock()), names: map[string]bool{}}
	defer func() {
		items, _ := rtm.List(context.Background())
		for _, item := range items {
			if err := rtm.Stop(context.Background(), item.ID); err != nil {
				t.Error(err)
			}
		}
	}()
	ag := &agent.Agent{ID: "storage-node", DataDir: filepath.Join(t.TempDir(), "agent"), Client: c, Token: c.Token, Runtime: rtm}
	manifest := func(replicas int, label string) []byte {
		return []byte(fmt.Sprintf("kind: App\nname: persistent\nimage: busybox:latest\nreplicas: %d\ncommand: [sh, -c, 'echo %s >> /data/history; exec sleep 600']\nstorage: {node: storage-node, source: %q, target: /data}\n", replicas, label, dir))
	}
	wait := func(status string) {
		t.Helper()
		for ctx.Err() == nil {
			if err := ag.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			asgs, err := c.ListAssignments(ctx)
			if err != nil {
				t.Fatal(err)
			}
			active, running := 0, false
			for _, a := range asgs {
				if api.Active(a.Status) {
					active++
					running = running || a.Status == api.StatusRunning
				}
			}
			if status == api.StatusStopped && active == 0 || status == api.StatusRunning && active == 1 && running {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("storage acceptance timed out")
	}
	if err := c.Apply(ctx, manifest(1, "first")); err != nil {
		t.Fatal(err)
	}
	wait(api.StatusRunning)
	if err := c.Apply(ctx, manifest(0, "first")); err != nil {
		t.Fatal(err)
	}
	wait(api.StatusStopped)
	if err := c.Apply(ctx, manifest(1, "second")); err != nil {
		t.Fatal(err)
	}
	wait(api.StatusRunning)
	for ctx.Err() == nil {
		body, err := os.ReadFile(filepath.Join(dir, "history"))
		if err == nil && string(body) == "first\nsecond\n" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("persistent data did not survive the stopped release change")
}
