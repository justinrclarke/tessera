package drill

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tessera/internal/agent"
	"tessera/internal/api"
	"tessera/internal/runtime"
)

type ownedDocker struct {
	*runtime.Docker
	mu    sync.Mutex
	names map[string]bool
}

func (r *ownedDocker) Start(ctx context.Context, spec runtime.Spec) (runtime.Container, error) {
	r.mu.Lock()
	r.names[spec.Name] = true
	r.mu.Unlock()
	return r.Docker.Start(ctx, spec)
}

func (r *ownedDocker) List(ctx context.Context) ([]runtime.Container, error) {
	items, err := r.Docker.List(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var own []runtime.Container
	for _, item := range items {
		if r.names[item.Name] {
			own = append(own, item)
		}
	}
	return own, nil
}

func (r *ownedDocker) Prune(context.Context) error { return nil }

func TestDockerReadinessRolloutSmoke(t *testing.T) {
	if os.Getenv("TESSERA_DOCKER_SMOKE") == "" {
		t.Skip("set TESSERA_DOCKER_SMOKE=1 to use a local Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	_, cl, cleanup := boot(time.Now())
	defer cleanup()
	rtm := &ownedDocker{Docker: runtime.NewDocker(runtime.DockerSock()), names: map[string]bool{}}
	if has, err := rtm.HasImage(ctx, "busybox:latest"); err != nil || !has {
		t.Fatalf("cached busybox image required: %v", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		items, _ := rtm.List(cleanupCtx)
		for _, item := range items {
			if err := rtm.Stop(cleanupCtx, item.ID); err != nil {
				t.Errorf("remove owned container: %v", err)
			}
		}
	}()
	ag := &agent.Agent{ID: "docker-http-" + api.NewID(), DataDir: filepath.Join(t.TempDir(), "agent"), Token: cl.Token, Client: cl, Runtime: rtm, Addr: "127.0.0.1"}
	port := freePort()
	manifest := func(label string, delay int, failed bool) []byte {
		command := fmt.Sprintf("mkdir -p /www; echo %s > /www/index.html; sleep %d; exec httpd -f -p 80 -h /www", label, delay)
		if failed {
			command = "exit 7"
		}
		return []byte(fmt.Sprintf("kind: App\nname: web\nimage: busybox:latest\ncommand: [sh, -c, '%s']\nports: [{container: 80}]\nhealth: {path: /, port: 80, timeout: 500ms, startup_timeout: 10s}\n", command))
	}
	body := append(manifest("old", 0, false), []byte(fmt.Sprintf("---\nkind: Route\nname: web\napp: web\nport: %d\ntarget_port: 80\n", port))...)
	if err := cl.Apply(ctx, body); err != nil {
		t.Fatal(err)
	}
	waitReady := func(generation int64) {
		t.Helper()
		for ctx.Err() == nil {
			if err := ag.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			app, err := cl.GetApp(ctx, "web")
			if err == nil && app.HealthyGeneration == generation {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("Docker readiness timed out")
	}
	waitReady(1)
	read := func() string {
		t.Helper()
		transport := &http.Transport{DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		resp, err := (&http.Client{Timeout: time.Second, Transport: transport}).Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("Docker route response %d: %v", resp.StatusCode, err)
		}
		return string(body)
	}
	if got := read(); got != "old\n" {
		t.Fatalf("initial route %q", got)
	}
	if err := cl.Apply(ctx, manifest("new", 2, false)); err != nil {
		t.Fatal(err)
	}
	if err := ag.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "old\n" {
		t.Fatalf("slow Docker startup displaced old container: %q", got)
	}
	for ctx.Err() == nil {
		if got := read(); got != "old\n" && got != "new\n" {
			t.Fatalf("rollout response %q", got)
		}
		if err := ag.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		app, _ := cl.GetApp(ctx, "web")
		if app.HealthyGeneration == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := ag.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "new\n" {
		t.Fatalf("ready cutover %q", got)
	}
	asgs, _ := cl.ListAssignments(ctx)
	current := ""
	for _, a := range asgs {
		if a.Generation == 2 && a.Status == api.StatusRunning {
			current = a.RuntimeID
		}
	}
	if current == "" {
		t.Fatal("missing running Docker identity")
	}
	if err := cl.Apply(ctx, manifest("broken", 0, true)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := ag.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if got := read(); got != "new\n" {
			t.Fatalf("failed update interrupted Docker service: %q", got)
		}
		app, _ := cl.GetApp(ctx, "web")
		if app.Generation == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	app, _ := cl.GetApp(ctx, "web")
	if app.Generation != 2 {
		t.Fatalf("failed Docker generation did not roll back: %+v", app)
	}
	asgs, _ = cl.ListAssignments(ctx)
	found := false
	for _, a := range asgs {
		if a.RuntimeID == current && a.Status == api.StatusRunning {
			found = true
		}
	}
	if !found {
		t.Fatal("rollback restarted healthy Docker service")
	}
}
