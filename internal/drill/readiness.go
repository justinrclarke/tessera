package drill

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"tessera/internal/agent"
	"tessera/internal/runtime"
)

type httpRuntime struct {
	*runtime.Fake
	services map[string]*httptest.Server
	ready    atomic.Bool
	starts   int
	stops    int
}

func (r *httpRuntime) Start(ctx context.Context, spec runtime.Spec) (runtime.Container, error) {
	if spec.Image == "invalid" {
		return runtime.Container{}, &runtime.StartError{Reason: "pull: invalid image"}
	}
	c, err := r.Fake.Start(ctx, spec)
	if err != nil {
		return c, err
	}
	r.starts++
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if spec.Image == "slow" && !r.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, spec.Image)
	}))
	r.services[c.ID] = service
	_, port, _ := net.SplitHostPort(service.Listener.Addr().String())
	c.HostPort, _ = strconv.Atoi(port)
	return c, nil
}

func (r *httpRuntime) List(ctx context.Context) ([]runtime.Container, error) {
	items, err := r.Fake.List(ctx)
	for i := range items {
		if service := r.services[items[i].ID]; service != nil {
			_, port, _ := net.SplitHostPort(service.Listener.Addr().String())
			items[i].HostPort, _ = strconv.Atoi(port)
		}
	}
	return items, err
}

func (r *httpRuntime) Stop(ctx context.Context, id string) error {
	r.stops++
	if service := r.services[id]; service != nil {
		service.Close()
		delete(r.services, id)
	}
	return r.Fake.Stop(ctx, id)
}

func readinessRollout() error {
	ctx := context.Background()
	_, cl, cleanup := boot(time.Now())
	defer cleanup()
	dir := tDir()
	defer os.RemoveAll(dir)
	rtm := &httpRuntime{Fake: runtime.NewFake(), services: map[string]*httptest.Server{}}
	defer func() {
		for _, service := range rtm.services {
			service.Close()
		}
	}()
	ag := &agent.Agent{ID: "http-worker", DataDir: filepath.Join(dir, "agent"), Token: cl.Token, Client: cl, Runtime: rtm, Addr: "127.0.0.1"}
	port := freePort()
	manifest := func(image string) []byte {
		return []byte(fmt.Sprintf("kind: App\nname: web\nimage: %s\nports: [{container: 80}]\nhealth: {path: /ready, port: 80, startup_timeout: 5s}\n", image))
	}
	if err := cl.Apply(ctx, append(manifest("old"), []byte(fmt.Sprintf("---\nkind: Route\nname: web\napp: web\nport: %d\ntarget_port: 80\n", port))...)); err != nil {
		return err
	}
	if err := ag.Tick(ctx); err != nil {
		return err
	}
	read := func(expected string) error {
		cl := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := cl.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK || string(body) != expected {
			return fmt.Errorf("route returned %d %q: %v, want %q", resp.StatusCode, body, err, expected)
		}
		return nil
	}
	if err := read("old"); err != nil {
		return err
	}
	before, err := cl.ListAssignments(ctx)
	if err != nil || len(before) != 1 {
		return fmt.Errorf("initial assignments: %+v %v", before, err)
	}
	if err := cl.Apply(ctx, manifest("slow")); err != nil {
		return err
	}
	for i := 0; i < 3; i++ {
		if err := ag.Tick(ctx); err != nil {
			return err
		}
		if err := read("old"); err != nil {
			return err
		}
	}
	app, err := cl.GetApp(ctx, "web")
	if err != nil || app.HealthyGeneration != 1 || rtm.starts != 2 || rtm.stops != 0 {
		return fmt.Errorf("slow startup replaced healthy service: %+v starts=%d stops=%d %v", app, rtm.starts, rtm.stops, err)
	}
	rtm.ready.Store(true)
	if err := ag.Tick(ctx); err != nil {
		return err
	}
	if err := ag.Tick(ctx); err != nil {
		return err
	}
	if err := read("slow"); err != nil {
		return err
	}
	app, err = cl.GetApp(ctx, "web")
	if err != nil || app.HealthyGeneration != 2 || rtm.stops != 1 {
		return fmt.Errorf("ready rollout did not complete: %+v stops=%d %v", app, rtm.stops, err)
	}
	if err := cl.Apply(ctx, manifest("invalid")); err != nil {
		return err
	}
	if err := ag.Tick(ctx); err != nil {
		return err
	}
	if err := ag.Tick(ctx); err != nil {
		return err
	}
	if err := read("slow"); err != nil {
		return err
	}
	app, err = cl.GetApp(ctx, "web")
	if err != nil || app.Image != "slow" || app.Generation != 2 || rtm.starts != 2 || rtm.stops != 1 {
		return fmt.Errorf("failed rollout lost previous service: %+v starts=%d stops=%d %v", app, rtm.starts, rtm.stops, err)
	}
	return nil
}
