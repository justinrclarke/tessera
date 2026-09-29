package drill

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/raft"

	"tessera/internal/agent"
	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/controller"
	"tessera/internal/runtime"
	"tessera/internal/store"
)

type replicaDrill struct {
	root       string
	servers    [3]*controller.Server
	transports [3]*raft.InmemTransport
	peers      []controller.ControllerPeer
}

func (d *replicaDrill) start(index int, bootstrap bool) error {
	_, d.transports[index] = raft.NewInmemTransportWithTimeout(raft.ServerAddress(d.peers[index].Address), 100*time.Millisecond)
	for i, other := range d.transports {
		if i != index && other != nil {
			d.transports[index].Connect(other.LocalAddr(), other)
			other.Connect(d.transports[index].LocalAddr(), d.transports[index])
		}
	}
	ln, err := net.Listen("tcp", strings.TrimPrefix(d.peers[index].URL, "http://"))
	if err != nil {
		return err
	}
	dir := filepath.Join(d.root, d.peers[index].ID)
	st, err := store.Open(filepath.Join(dir, "tessera.db"))
	if err != nil {
		ln.Close()
		return err
	}
	srv, err := controller.New(st, controller.Config{DataDir: dir, ID: d.peers[index].ID, Token: "drill-token", URL: d.peers[index].URL, Replicas: &controller.ReplicaConfig{Peers: d.peers, Transport: d.transports[index], Bootstrap: bootstrap, HeartbeatTimeout: 100 * time.Millisecond}})
	if err != nil {
		ln.Close()
		st.Close()
		return err
	}
	d.servers[index] = srv
	go func() { _ = srv.Serve(context.Background(), ln) }()
	return nil
}

func (d *replicaDrill) stop(index int) {
	if srv := d.servers[index]; srv != nil {
		_ = srv.Close()
		_ = srv.Store.Close()
		d.servers[index] = nil
	}
}

func (d *replicaDrill) leader() (int, error) {
	index := -1
	err := waitReplicaDrill(func() bool {
		for i, srv := range d.servers {
			if srv != nil && srv.Leading() {
				index = i
				return true
			}
		}
		return false
	})
	return index, err
}

func waitReplicaDrill(fn func() bool) error {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("replica condition timed out")
}

type lostUpdateResponse struct {
	base http.RoundTripper
	kill func()
}

func (r *lostUpdateResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := r.base.RoundTrip(req)
	if err == nil && r.kill != nil && req.Method == http.MethodPost && req.URL.Path == "/v1/apply" && response.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		kill := r.kill
		r.kill = nil
		kill()
		return nil, io.ErrUnexpectedEOF
	}
	return response, err
}

type trackedRuntime struct {
	*runtime.Fake
	starts map[string]int
	stops  map[string]int
}

func (r *trackedRuntime) Start(ctx context.Context, spec runtime.Spec) (runtime.Container, error) {
	r.starts[spec.Name]++
	return r.Fake.Start(ctx, spec)
}

func (r *trackedRuntime) Stop(ctx context.Context, id string) error {
	r.stops[id]++
	return r.Fake.Stop(ctx, id)
}

func replicatedController() error {
	root, err := os.MkdirTemp("", "tessera-replicas-")
	if err != nil {
		return err
	}
	d := &replicaDrill{root: root}
	defer func() {
		for i := range d.servers {
			d.stop(i)
		}
		os.RemoveAll(root)
	}()
	for i := range d.servers {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		id := fmt.Sprintf("replica-%d", i)
		d.peers = append(d.peers, controller.ControllerPeer{ID: id, Address: id, URL: "http://" + ln.Addr().String()})
		ln.Close()
	}
	for i := range d.servers {
		if err := d.start(i, i == 0); err != nil {
			return err
		}
	}
	leader, err := d.leader()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl := client.New(d.peers[leader].URL, "drill-token")
	if err := cl.Apply(ctx, []byte("kind: App\nname: web\nimage: v1\n---\nkind: App\nname: steady\nimage: steady\n")); err != nil {
		return err
	}
	fake := &trackedRuntime{Fake: runtime.NewFake(), starts: map[string]int{}, stops: map[string]int{}}
	ag := &agent.Agent{ID: "node", DataDir: filepath.Join(root, "agent"), Token: "drill-token", Client: cl, Runtime: fake, Addr: "127.0.0.1"}
	if err := ag.Tick(ctx); err != nil {
		return err
	}
	assignments, err := cl.ListAssignments(ctx)
	if err != nil {
		return err
	}
	steady := ""
	for _, assignment := range assignments {
		if assignment.App == "steady" && assignment.Status == api.StatusRunning {
			steady = assignment.RuntimeID
		}
	}
	if steady == "" {
		return fmt.Errorf("steady workload did not start")
	}
	oldEpoch := d.servers[leader].Epoch()
	cl.HTTP.Transport = &lostUpdateResponse{base: cl.HTTP.Transport, kill: func() { d.stop(leader) }}
	if err := cl.Apply(ctx, []byte("kind: App\nname: web\nimage: v2\n")); err != nil {
		return fmt.Errorf("update retry after leader loss: %w", err)
	}
	next, err := d.leader()
	if err != nil {
		return err
	}
	if d.servers[next].Epoch() <= oldEpoch {
		return fmt.Errorf("leader epoch did not advance")
	}
	for i := 0; i < 2; i++ {
		if err := ag.Tick(ctx); err != nil {
			return err
		}
	}
	if fake.starts[steady] != 1 || fake.stops[steady] != 0 {
		return fmt.Errorf("leader loss restarted the steady workload: starts=%d stops=%d", fake.starts[steady], fake.stops[steady])
	}
	app, err := cl.GetApp(ctx, "web")
	if err != nil || app.Generation != 2 || app.Image != "v2" {
		return fmt.Errorf("update committed more than once: %+v %v", app, err)
	}
	if err := d.start(leader, false); err != nil {
		return err
	}
	if err := waitReplicaDrill(func() bool {
		app, err := d.servers[leader].Store.GetApp("web")
		return err == nil && app.Generation == 2 && d.servers[leader].Epoch() == d.servers[next].Epoch()
	}); err != nil {
		return fmt.Errorf("restarted replica did not catch up: %w", err)
	}
	d.transports[leader].DisconnectAll()
	for i, transport := range d.transports {
		if i != leader {
			transport.Disconnect(d.transports[leader].LocalAddr())
		}
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, d.peers[leader].URL+"/v1/apply", strings.NewReader("kind: App\nname: web\nimage: partitioned\n"))
	request.Header.Set("Authorization", "Bearer drill-token")
	raw := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := raw.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect && response.StatusCode != http.StatusServiceUnavailable {
		return fmt.Errorf("partitioned old leader accepted a write: %d", response.StatusCode)
	}
	app, err = d.servers[leader].Store.GetApp("web")
	if err != nil || app.Generation != 2 || app.Image != "v2" {
		return fmt.Errorf("partition changed committed state: %+v %v", app, err)
	}
	ag.Client = client.New("http://127.0.0.1:1", "drill-token")
	ag.LostAt = time.Now().Add(-time.Minute)
	if promoted, err := ag.PromoteForTest(ctx); err != nil || promoted {
		return fmt.Errorf("replica agent bypassed quorum: promoted=%v err=%v", promoted, err)
	}
	backup := filepath.Join(root, "backup.db")
	if err := d.servers[next].Store.Backup(backup); err != nil {
		return err
	}
	newEpoch := d.servers[next].Epoch()
	for i := range d.servers {
		d.stop(i)
	}
	restoredDir := filepath.Join(root, "restored")
	if err := store.RestoreBackup(backup, filepath.Join(restoredDir, "tessera.db"), newEpoch+1); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(restoredDir, "tessera.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	restored, err := controller.New(st, controller.Config{DataDir: restoredDir})
	if err != nil {
		return err
	}
	defer restored.Close()
	if restored.Epoch() <= newEpoch || restored.Token != "drill-token" {
		return fmt.Errorf("restored replica backup lost fencing or credentials")
	}
	old, err := st.History("web", 1)
	if err != nil || old.Image != "v1" {
		return fmt.Errorf("restored replica backup lost release history: %+v %v", old, err)
	}
	assignments, err = st.ListAssignments()
	if err != nil {
		return err
	}
	for _, assignment := range assignments {
		if assignment.RuntimeID == steady && assignment.Status == api.StatusRunning && assignment.Epoch == restored.Epoch() {
			return nil
		}
	}
	return fmt.Errorf("restored backup lost the running workload identity")
}
