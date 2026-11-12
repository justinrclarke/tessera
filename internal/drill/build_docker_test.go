package drill

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tessera/internal/agent"
	"tessera/internal/api"
	"tessera/internal/runtime"
	"tessera/internal/stackbuild"
)

func TestPublishedBuildSecondClusterAndDigestRollback(t *testing.T) {
	if os.Getenv("TESSERA_DOCKER_REGISTRY_SMOKE") == "" {
		t.Skip("set TESSERA_DOCKER_REGISTRY_SMOKE=1 to use a local Docker daemon and registry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	out, err := run(ctx, "docker", "run", "--rm", "-d", "-p", "127.0.0.1::5000", "registry:3.1")
	if err != nil {
		t.Fatalf("start disposable registry: %s %v", out, err)
	}
	registry := strings.TrimSpace(string(out))
	defer run(context.Background(), "docker", "rm", "-f", registry)
	out, err = run(ctx, "docker", "port", registry, "5000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	address := strings.TrimSpace(string(out))
	for attempts := 0; ; attempts++ {
		resp, err := http.Get("http://" + address + "/v2/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if attempts > 30 {
			t.Fatal("registry did not become ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	dir := t.TempDir()
	repo := address + "/tessera-accept-" + api.NewID()
	manifest := []byte(fmt.Sprintf("kind: App\nname: built\nbuild:\n  base: alpine:3.21\n  repository: %s\n  push: true\n  command: [sh, -c, 'cat version; exec sleep 3600']\n", repo))
	build := func(version string) ([]byte, string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "version"), []byte(version), 0600); err != nil {
			t.Fatal(err)
		}
		body, images, err := stackbuild.Prepare(ctx, filepath.Join(dir, "app.yaml"), manifest, run)
		if err != nil || len(images) != 1 {
			t.Fatalf("build: %s %v", body, err)
		}
		id, err := run(ctx, "docker", "image", "inspect", "--format={{.Id}}", images[0])
		if err != nil {
			t.Fatal(err)
		}
		tag := repo + ":sha256-" + strings.TrimPrefix(strings.TrimSpace(string(id)), "sha256:")
		t.Cleanup(func() {
			run(context.Background(), "docker", "image", "rm", "--force", tag, images[0])
		})
		return body, images[0]
	}
	first, digest := build("first\n")
	for cluster := range 2 {
		_, cl, cleanup := bootWithClock(time.Now)
		t.Cleanup(cleanup)
		rtm := &ownedDocker{Docker: runtime.NewDocker(runtime.DockerSock()), names: map[string]bool{}}
		t.Cleanup(func() {
			items, _ := rtm.List(context.Background())
			for _, item := range items {
				if err := rtm.Stop(context.Background(), item.ID); err != nil {
					t.Errorf("remove owned container: %v", err)
				}
			}
		})
		worker := &agent.Agent{ID: "build-" + api.NewID(), DataDir: filepath.Join(t.TempDir(), "agent"), Token: cl.Token, Client: cl, Runtime: rtm}
		if cluster == 1 {
			id, err := run(ctx, "docker", "image", "inspect", "--format={{.Id}}", digest)
			if err != nil {
				t.Fatal(err)
			}
			tag := repo + ":sha256-" + strings.TrimPrefix(strings.TrimSpace(string(id)), "sha256:")
			if out, err := run(ctx, "docker", "image", "rm", "--force", tag, digest); err != nil {
				t.Fatalf("remove local references before second cluster pull: %s %v", out, err)
			}
			if _, err := run(ctx, "docker", "image", "inspect", digest); err == nil {
				t.Fatal("second cluster still has a local digest reference")
			}
		}
		if err := cl.Apply(ctx, first); err != nil {
			t.Fatal(err)
		}
		if err := worker.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		app, err := cl.GetApp(ctx, "built")
		if err != nil || app.HealthyGeneration != 1 || app.Image != digest || app.Artifact == nil {
			assignments, _ := cl.ListAssignments(ctx)
			t.Fatalf("cluster %d missing digest release: %+v %v assignments=%+v", cluster, app, err, assignments)
		}
		if cluster == 0 {
			continue
		}
		second, newDigest := build("second\n")
		if newDigest == digest {
			t.Fatal("new source did not produce a new published digest")
		}
		second = append(second, []byte("command: [/tessera-intentionally-missing-command]\n")...)
		if err := cl.Apply(ctx, second); err != nil {
			t.Fatal(err)
		}
		for attempts := 0; attempts < 10; attempts++ {
			if err := worker.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			app, err = cl.GetApp(ctx, "built")
			if err != nil {
				t.Fatal(err)
			}
			if app.Generation == 1 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if app.Generation != 1 || app.Image != digest || app.Artifact.Image != digest || app.HealthyGeneration != 1 {
			t.Fatalf("failed published release did not restore prior digest and provenance: %+v", app)
		}
		if err := worker.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		items, err := rtm.List(ctx)
		if err != nil || len(items) != 1 || !items[0].Running {
			t.Fatalf("rollback disrupted healthy container: %+v %v", items, err)
		}
	}
}
