package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
)

func TestGatewayCachesDuringOutageButClearsRemovedRoutes(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "healthy") }))
	defer backend.Close()
	var status atomic.Int64
	var epoch atomic.Uint64
	status.Store(http.StatusOK)
	epoch.Store(3)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.URL.Path != "/v1/routes/web/backends" {
			t.Error("invalid route request")
		}
		if code := status.Load(); code != http.StatusOK {
			w.WriteHeader(int(code))
			return
		}
		json.NewEncoder(w).Encode(api.RouteBackends{Epoch: epoch.Load(), Backends: []string{backend.Listener.Addr().String()}})
	}))
	defer controller.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, text, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(text)
	ln.Close()
	g := &Gateway{Client: client.New(controller.URL, "token"), Route: "web", Port: port}
	defer g.Close()
	if err := g.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	read := func() {
		t.Helper()
		resp, err := (&http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Get("http://127.0.0.1:" + text)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "healthy" {
			t.Fatalf("response %q", body)
		}
	}
	read()
	status.Store(http.StatusInternalServerError)
	if err := g.Sync(context.Background()); err == nil {
		t.Fatal("refresh error hidden")
	}
	read()
	status.Store(http.StatusOK)
	epoch.Store(2)
	if err := g.Sync(context.Background()); err == nil {
		t.Fatal("accepted stale epoch")
	}
	read()
	status.Store(http.StatusNotFound)
	if err := g.Sync(context.Background()); err == nil {
		t.Fatal("missing route hidden")
	}
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+text, time.Second); err == nil {
		c.Close()
		t.Fatal("deleted route kept serving")
	}
}
