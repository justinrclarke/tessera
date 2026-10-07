package controller

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tessera/internal/client"
	"tessera/internal/store"
	"tessera/internal/survive"

	"github.com/hashicorp/raft"
)

type replicaTestCluster struct {
	servers    []*Server
	transports []*raft.InmemTransport
	peers      []ControllerPeer
	dirs       []string
	stopped    []bool
	tcp        bool
}

func newReplicaTestCluster(t *testing.T) *replicaTestCluster {
	return newReplicaTestClusterTransport(t, false)
}

func newReplicaTestClusterTransport(t *testing.T, tcp bool) *replicaTestCluster {
	t.Helper()
	c := &replicaTestCluster{servers: make([]*Server, 3), transports: make([]*raft.InmemTransport, 3), dirs: make([]string, 3), stopped: make([]bool, 3), tcp: tcp}
	listeners := make([]net.Listener, 3)
	for i := range c.servers {
		id := fmt.Sprintf("controller-%d", i)
		address := id
		if tcp {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address = ln.Addr().String()
			ln.Close()
		} else {
			_, c.transports[i] = raft.NewInmemTransportWithTimeout(raft.ServerAddress(id), 100*time.Millisecond)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners[i] = ln
		c.peers = append(c.peers, ControllerPeer{ID: id, Address: address, URL: "http://" + ln.Addr().String()})
		c.dirs[i] = filepath.Join(t.TempDir(), id)
	}
	for i, transport := range c.transports {
		if transport == nil {
			continue
		}
		for j, other := range c.transports {
			if i != j {
				transport.Connect(other.LocalAddr(), other)
			}
		}
	}
	t.Cleanup(func() {
		for i := range c.servers {
			c.stop(i)
		}
	})
	for i := range c.servers {
		st, err := store.Open(filepath.Join(c.dirs[i], "tessera.db"))
		if err != nil {
			t.Fatal(err)
		}
		var transport raft.Transport
		if c.transports[i] != nil {
			transport = c.transports[i]
		}
		srv, err := New(st, Config{DataDir: c.dirs[i], Token: "replica-token", ID: c.peers[i].ID, URL: c.peers[i].URL, Replicas: &ReplicaConfig{Listen: c.peers[i].Address, Peers: c.peers, Transport: transport, Bootstrap: i == 0, HeartbeatTimeout: 100 * time.Millisecond, RequestTimeout: time.Second}})
		if err != nil {
			st.Close()
			t.Fatal(err)
		}
		c.servers[i] = srv
		go func() { _ = srv.Serve(context.Background(), listeners[i]) }()
	}
	for i := range c.servers {
		cl := client.New(c.peers[i].URL, "replica-token")
		waitReplica(t, func() bool { return cl.Health(context.Background()) == nil })
	}
	c.leader(t, -1)
	return c
}

func (c *replicaTestCluster) restart(t *testing.T, index int) {
	t.Helper()
	var transport raft.Transport
	if !c.tcp {
		_, c.transports[index] = raft.NewInmemTransportWithTimeout(raft.ServerAddress(c.peers[index].Address), 100*time.Millisecond)
		transport = c.transports[index]
		for i, other := range c.transports {
			if i != index {
				c.transports[index].Connect(other.LocalAddr(), other)
				other.Connect(c.transports[index].LocalAddr(), c.transports[index])
			}
		}
	}
	st, err := store.Open(filepath.Join(c.dirs[index], "tessera.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, Config{DataDir: c.dirs[index], Token: "replica-token", ID: c.peers[index].ID, URL: c.peers[index].URL, Replicas: &ReplicaConfig{Listen: c.peers[index].Address, Peers: c.peers, Transport: transport, HeartbeatTimeout: 100 * time.Millisecond, RequestTimeout: time.Second}})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", strings.TrimPrefix(c.peers[index].URL, "http://"))
	if err != nil {
		srv.Close()
		st.Close()
		t.Fatal(err)
	}
	c.servers[index], c.stopped[index] = srv, false
	go func() { _ = srv.Serve(context.Background(), ln) }()
	cl := client.New(c.peers[index].URL, "replica-token")
	waitReplica(t, func() bool { return cl.Health(context.Background()) == nil })
}

func TestTCPReplicasRecoverSnapshotAndLogAfterFullRestart(t *testing.T) {
	c := newReplicaTestClusterTransport(t, true)
	leader := c.leader(t, -1)
	cl := client.New(c.peers[leader].URL, "replica-token")
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: v1\n")); err != nil {
		t.Fatal(err)
	}
	waitReplica(t, func() bool {
		for _, srv := range c.servers {
			if app, err := srv.Store.GetApp("web"); err != nil || app.Generation != 1 {
				return false
			}
		}
		return true
	})
	for _, srv := range c.servers {
		if err := srv.replica.raft.Snapshot().Error(); err != nil {
			t.Fatal(err)
		}
	}
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: v2\n")); err != nil {
		t.Fatal(err)
	}
	waitReplica(t, func() bool {
		for _, srv := range c.servers {
			if app, err := srv.Store.GetApp("web"); err != nil || app.Generation != 2 {
				return false
			}
		}
		return true
	})
	epoch := c.servers[leader].Epoch()
	for i := range c.servers {
		c.stop(i)
	}
	for i := range c.servers {
		c.restart(t, i)
	}
	leader = c.leader(t, -1)
	if c.servers[leader].Epoch() <= epoch {
		t.Fatal("restart reused the old leader epoch")
	}
	app, err := cl.GetApp(context.Background(), "web")
	if err != nil || app.Image != "v2" || app.Generation != 2 {
		t.Fatalf("snapshot and log recovery: %+v, %v", app, err)
	}
	for _, srv := range c.servers {
		old, err := srv.Store.History("web", 1)
		if err != nil || old.Image != "v1" {
			t.Fatalf("restart lost rollback history: %+v, %v", old, err)
		}
	}
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: v3\n")); err != nil {
		t.Fatal(err)
	}
	c.stop(leader)
	c.leader(t, leader)
	app, err = cl.GetApp(context.Background(), "web")
	if err != nil || app.Image != "v3" || app.Generation != 3 {
		t.Fatalf("TCP failover lost committed update: %+v, %v", app, err)
	}
}

func (c *replicaTestCluster) stop(index int) {
	if c.servers[index] == nil || c.stopped[index] {
		return
	}
	c.stopped[index] = true
	_ = c.servers[index].Close()
	_ = c.servers[index].Store.Close()
}

func (c *replicaTestCluster) leader(t *testing.T, exclude int) int {
	t.Helper()
	index := -1
	waitReplica(t, func() bool {
		for i, srv := range c.servers {
			if i != exclude && !c.stopped[i] && srv.Leading() {
				index = i
				return true
			}
		}
		return false
	})
	return index
}

func waitReplica(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("replica condition timed out")
}

func TestReplicatedWritesRedirectAndSurviveLeaderLoss(t *testing.T) {
	c := newReplicaTestCluster(t)
	leader := c.leader(t, -1)
	follower := (leader + 1) % 3
	request, err := http.NewRequest(http.MethodPost, c.peers[follower].URL+"/v1/apply", strings.NewReader("kind: App\nname: web\nimage: v1\n"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer replica-token")
	raw := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := raw.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || !strings.HasPrefix(response.Header.Get("Location"), c.peers[leader].URL) {
		t.Fatalf("follower accepted a write: %d, %s", response.StatusCode, response.Header.Get("Location"))
	}
	cl := client.New(c.peers[follower].URL, "replica-token")
	manifest := []byte("kind: App\nname: web\nimage: v1\n---\nkind: Config\nname: settings\ndata: {mode: test}\n---\nkind: Secret\nname: credential\ndata: {password: test-only}\n")
	if err := cl.Apply(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	waitReplica(t, func() bool {
		for _, srv := range c.servers {
			app, err := srv.Store.GetApp("web")
			if err != nil || app.Generation != 1 {
				return false
			}
			if _, err := srv.Store.GetSecret("credential"); err != nil {
				return false
			}
		}
		return true
	})
	if len(cl.Controllers()) != 3 {
		t.Fatal("client did not learn replica addresses")
	}
	epoch := c.servers[leader].Epoch()
	c.stop(leader)
	next := c.leader(t, leader)
	if c.servers[next].Epoch() <= epoch {
		t.Fatal("new leader did not advance the fencing epoch")
	}
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: v2\n")); err != nil {
		t.Fatal(err)
	}
	app, err := cl.GetApp(context.Background(), "web")
	if err != nil || app.Generation != 2 {
		t.Fatalf("failover app %+v: %v", app, err)
	}
	for i, srv := range c.servers {
		if c.stopped[i] {
			continue
		}
		waitReplica(t, func() bool { a, err := srv.Store.GetApp("web"); return err == nil && a.Generation == 2 })
	}
}

func TestControllerStatusRemainsLocalAndAuthenticatedWithoutMajority(t *testing.T) {
	c := newReplicaTestCluster(t)
	leader := c.leader(t, -1)
	for i, peer := range c.peers {
		cl := client.New(peer.URL, "replica-token")
		status, err := cl.ControllerStatus(context.Background())
		if err != nil || status.ID != peer.ID || status.Writable != (i == leader) || status.Epoch == 0 {
			t.Fatalf("controller status redirected or wrong: %+v %v", status, err)
		}
		if _, err := client.New(peer.URL, "wrong").ControllerStatus(context.Background()); err == nil {
			t.Fatal("controller state exposed without authentication")
		}
	}
	c.transports[leader].DisconnectAll()
	for i, transport := range c.transports {
		if i != leader {
			transport.Disconnect(c.transports[leader].LocalAddr())
		}
	}
	c.leader(t, leader)
	cl := client.New(c.peers[leader].URL, "replica-token")
	status, err := cl.ControllerStatus(context.Background())
	if err != nil || status.ID != c.peers[leader].ID || status.Writable {
		t.Fatalf("minority status unavailable or writable: %+v %v", status, err)
	}
	for _, dir := range c.dirs {
		info, err := os.Stat(filepath.Join(dir, "raft-snapshots"))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("Raft snapshots expose secrets: %v %v", info, err)
		}
	}
}

func TestMinorityCannotMutateAnyControllerEndpoint(t *testing.T) {
	c := newReplicaTestCluster(t)
	leader := c.leader(t, -1)
	c.transports[leader].DisconnectAll()
	for i, transport := range c.transports {
		if i != leader {
			transport.Disconnect(c.transports[leader].LocalAddr())
		}
	}
	c.leader(t, leader)
	srv := c.servers[leader]
	before, err := srv.Store.ExportState()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/apply", "/v1/nodes/register", "/v1/nodes/n/heartbeat", "/v1/assignments/a/status", "/v1/actions/a/confirm", "/v1/actions/a/result", "/v1/act"} {
		req, err := http.NewRequest(http.MethodPost, c.peers[leader].URL+path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer replica-token")
		response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusTemporaryRedirect && response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("minority accepted %s: %d", path, response.StatusCode)
		}
	}
	after, err := srv.Store.ExportState()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("minority changed state: %v", err)
	}
}

func TestReplicatedRequestIDAndSnapshotKeepExactState(t *testing.T) {
	c := newReplicaTestCluster(t)
	leader := c.leader(t, -1)
	post := func(endpoint, image string) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, endpoint+"/v1/apply", strings.NewReader("kind: App\nname: web\nimage: "+image+"\n"))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer replica-token")
		request.Header.Set("X-Tessera-Request-ID", "same-logical-request")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("request %d: %s", response.StatusCode, body)
		}
	}
	post(c.peers[leader].URL, "v1")
	cl := client.New(c.peers[leader].URL, "replica-token")
	if err := cl.Apply(context.Background(), []byte("kind: App\nname: web\nimage: v2\n")); err != nil {
		t.Fatal(err)
	}
	post(c.peers[leader].URL, "v1")
	app, err := cl.GetApp(context.Background(), "web")
	if err != nil || app.Image != "v2" || app.Generation != 2 {
		t.Fatalf("retry reverted newer release: %+v, %v", app, err)
	}
	if err := c.servers[leader].replica.raft.Snapshot().Error(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := cl.Snapshot(context.Background())
	if err != nil || !survive.Verify(cl.Token, []byte(snapshot.Body), snapshot.Sig) {
		t.Fatalf("signed snapshot: %v", err)
	}
	waitReplica(t, func() bool {
		for _, srv := range c.servers {
			body, sig, err := srv.Store.RawSnapshot()
			if err != nil || body != snapshot.Body || sig != snapshot.Sig {
				return false
			}
		}
		return true
	})
	fsmSnapshot, err := (&controllerFSM{server: c.servers[leader]}).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	statements := fsmSnapshot.(*controllerSnapshot).statements
	st, err := store.Open(filepath.Join(t.TempDir(), "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.RestoreState(statements); err != nil {
		t.Fatal(err)
	}
	old, err := st.History("web", 1)
	if err != nil || old.Image != "v1" {
		t.Fatalf("replica snapshot lost history: %+v, %v", old, err)
	}
	body, sig, err := st.RawSnapshot()
	if err != nil || body != snapshot.Body || sig != snapshot.Sig {
		t.Fatal("replica snapshot changed signed raw body")
	}
	encoded, err := json.Marshal(statements)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("state snapshot: %v", err)
	}
}

func TestRaftStreamAuthenticatesClusterToken(t *testing.T) {
	server, err := newRaftStream("127.0.0.1:0", "shared-token")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	peer, err := newRaftStream("127.0.0.1:0", "shared-token")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := server.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		body := make([]byte, 5)
		_, err = io.ReadFull(conn, body)
		if err == nil && !bytes.Equal(body, []byte("hello")) {
			err = fmt.Errorf("unexpected TLS payload")
		}
		done <- err
	}()
	conn, err := peer.Dial(raft.ServerAddress(server.Addr().String()), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if conn.(*tls.Conn).ConnectionState().Version != tls.VersionTLS13 {
		t.Fatal("Raft connection did not use TLS 1.3")
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := server.Accept()
		if err == nil {
			conn.Close()
		}
	}()
	wrong, err := newRaftStream("127.0.0.1:0", "different-token")
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if conn, err := wrong.Dial(raft.ServerAddress(server.Addr().String()), time.Second); err == nil {
		conn.Close()
		t.Fatal("different cluster token authenticated")
	}
}
