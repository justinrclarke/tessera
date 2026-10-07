package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type interruptedBody struct{}

func (interruptedBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (interruptedBody) Close() error             { return nil }

func TestInterruptedResponseRetriesSameMutation(t *testing.T) {
	cl := New("http://a", "token")
	cl.SetControllers([]string{"http://a", "http://b", "http://c"})
	var ids, bodies []string
	cl.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.Header.Get("X-Tessera-Request-ID"))
		bodies = append(bodies, string(body))
		var response io.ReadCloser = io.NopCloser(strings.NewReader(`{}`))
		if len(ids) == 1 {
			response = interruptedBody{}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: response}, nil
	})
	if err := cl.Apply(context.Background(), []byte("manifest")); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] || bodies[0] != "manifest" || bodies[1] != bodies[0] {
		t.Fatalf("retry changed mutation: ids=%v bodies=%v", ids, bodies)
	}
}

func TestImageTransfersKeepCallerDeadline(t *testing.T) {
	cl := New("http://a", "token")
	cl.SetControllers([]string{"http://a", "http://b", "http://c"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	cl.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if got, ok := r.Context().Deadline(); !ok || !got.Equal(deadline) {
			t.Fatalf("image transfer shortened caller deadline: %s", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("image"))}, nil
	})
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		response, err := cl.do(ctx, method, "/v1/images?ref=large", strings.NewReader("image"))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
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
