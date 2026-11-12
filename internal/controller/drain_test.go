package controller

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
)

func TestRolloutDrainsHTTPBeforeStoppingOldAssignment(t *testing.T) {
	s, c := standaloneClient(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := c.Register(ctx, client.RegisterRequest{ID: "node", Addr: "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release; io.WriteString(w, "old") }))
	defer old.Close()
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "new") }))
	defer next.Close()
	portOf := func(address string) int {
		_, text, _ := net.SplitHostPort(address)
		n, _ := strconv.Atoi(text)
		return n
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := portOf(ln.Addr().String())
	ln.Close()
	if err := c.Apply(ctx, []byte(fmt.Sprintf("kind: App\nname: web\nimage: old\ndrain_timeout: 1s\n---\nkind: Route\nname: web\napp: web\nport: %d\n", port))); err != nil {
		t.Fatal(err)
	}
	asgs, _ := s.Store.ListAssignments()
	oldID := asgs[0].ID
	if err := c.Report(ctx, oldID, client.StatusReport{Status: api.StatusRunning, HostPort: portOf(old.Listener.Addr().String())}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		response, err := (&http.Client{Timeout: 3 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d", port))
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil || string(body) != "old" {
				err = fmt.Errorf("old request returned %q: %v", body, readErr)
			}
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old request did not start")
	}
	if err := c.Apply(ctx, []byte("kind: App\nname: web\nimage: new\ndrain_timeout: 1s\n")); err != nil {
		close(release)
		t.Fatal(err)
	}
	asgs, _ = s.Store.ListAssignments()
	for _, a := range asgs {
		if a.ID != oldID {
			if err := c.Report(ctx, a.ID, client.StatusReport{Status: api.StatusRunning, HostPort: portOf(next.Listener.Addr().String())}); err != nil {
				close(release)
				t.Fatal(err)
			}
		}
	}
	oldAssignment, _ := s.Store.GetAssignment(oldID)
	if oldAssignment.Status != api.StatusDraining {
		close(release)
		t.Fatalf("old workload not draining: %+v", oldAssignment)
	}
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "new" {
		close(release)
		t.Fatal("new requests reached draining workload")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	oldAssignment, _ = s.Store.GetAssignment(oldID)
	if oldAssignment.Status != api.StatusStopped {
		t.Fatal("old workload not stopped after bounded drain")
	}
}
