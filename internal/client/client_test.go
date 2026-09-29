package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFailoverReplaysBodyAndRequestIDOnlyToClusterPeers(t *testing.T) {
	for _, foreignRedirect := range []bool{false, true} {
		name := "leader redirect"
		if foreignRedirect {
			name = "foreign redirect"
		}
		t.Run(name, func(t *testing.T) {
			foreignCalled := false
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				foreignCalled = true
				w.WriteHeader(http.StatusOK)
			}))
			defer foreign.Close()
			var ids, bodies, tokens []string
			record := func(r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				ids = append(ids, r.Header.Get("X-Tessera-Request-ID"))
				bodies = append(bodies, string(body))
				tokens = append(tokens, r.Header.Get("Authorization"))
			}
			leader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				record(r)
				_, _ = w.Write([]byte("{}"))
			}))
			defer leader.Close()
			var peers []string
			follower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				record(r)
				body, _ := json.Marshal(peers)
				w.Header().Set("X-Tessera-Controllers", string(body))
				target := leader.URL
				if foreignRedirect {
					target = foreign.URL
				}
				w.Header().Set("Location", target+r.URL.RequestURI())
				w.WriteHeader(http.StatusTemporaryRedirect)
			}))
			defer follower.Close()
			peers = []string{follower.URL, leader.URL, "http://127.0.0.1:1"}
			cl := New(follower.URL, "cluster-token")
			manifest := "kind: App\nname: web\nimage: v2\n"
			if err := cl.Apply(context.Background(), []byte(manifest)); err != nil {
				t.Fatal(err)
			}
			if foreignCalled || len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] || bodies[0] != manifest || bodies[1] != manifest || tokens[0] != "Bearer cluster-token" || tokens[1] != tokens[0] {
				t.Fatalf("unsafe or changed retry: ids=%v bodies=%v tokens=%v foreign=%v", ids, bodies, tokens, foreignCalled)
			}
			if cl.Endpoint() != leader.URL || len(cl.Controllers()) != 3 {
				t.Fatal("did not retain the elected leader and peer addresses")
			}
		})
	}
}

func TestSavedPeersCannotOverrideAnotherClusterAndRetryHonorsDeadline(t *testing.T) {
	cl := New("http://127.0.0.1:1", "token")
	cl.SetControllers([]string{"http://other-a", "http://other-b", "http://other-c"})
	if len(cl.Controllers()) != 0 {
		t.Fatal("accepted saved addresses belonging to another cluster")
	}
	cl.SetControllers([]string{cl.Endpoint(), "http://127.0.0.1:2", "http://127.0.0.1:3"})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := cl.Apply(ctx, []byte("kind: App\nname: web\nimage: v1\n")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retry ignored caller deadline: %v", err)
	}
}
