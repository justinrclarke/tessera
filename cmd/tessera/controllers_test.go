package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tessera/internal/api"
	"tessera/internal/client"
)

func TestGetControllersShowsFollowersAndUnreachablePeers(t *testing.T) {
	var peers []string
	follower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/controllers" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("invalid status request: %s", r.URL)
		}
		body, _ := json.Marshal(peers)
		w.Header().Set("X-Tessera-Controllers", string(body))
		_ = json.NewEncoder(w).Encode(api.ControllerStatus{ID: "follower", Role: "Follower", Epoch: 2})
	}))
	defer follower.Close()
	leader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.ControllerStatus{ID: "leader", Role: "Leader", Epoch: 2, Writable: true})
	}))
	defer leader.Close()
	peers = []string{follower.URL, leader.URL, "http://127.0.0.1:1"}
	cl := client.New(follower.URL, "test-token")
	var out bytes.Buffer
	if err := getControllers(context.Background(), cl, &out); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Follower", "Leader", "unreachable", "true", "false"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("missing %s in %s", text, out.String())
		}
	}
}
