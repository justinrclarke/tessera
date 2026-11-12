package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/gateway"
	rt "tessera/internal/runtime"
)

type acceptanceHost struct {
	SSH     string `json:"ssh"`
	Address string `json:"address"`
	dir     string
	id      string
	url     string
	raft    string
}

func acceptanceQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (h acceptanceHost) command(ctx context.Context, script string) *exec.Cmd {
	if h.SSH == "" {
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
	return exec.CommandContext(ctx, "ssh", "-T", "-oBatchMode=yes", "-oConnectTimeout=5", "--", h.SSH, "sh -c "+acceptanceQuote(script))
}

func (h acceptanceHost) run(ctx context.Context, script string, input io.Reader) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := h.command(ctx, script)
	cmd.Stdin = input
	return cmd.CombinedOutput()
}

type acceptanceProcess struct {
	host acceptanceHost
	name string
	done chan error
}

func (p *acceptanceProcess) stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pidFile := acceptanceQuote(filepath.Join(p.host.dir, p.name+".pid"))
	binary := acceptanceQuote(filepath.Join(p.host.dir, "tessera"))
	script := "set -eu; pid=$(cat " + pidFile + "); case $pid in ''|*[!0-9]*) exit 1;; esac; args=$(ps -ww -p \"$pid\" -o args= || true); case $args in *" + binary + "*) kill -KILL \"$pid\";; '') ;; *) exit 1;; esac"
	if output, err := p.host.run(ctx, script, nil); err != nil {
		return fmt.Errorf("stop %s/%s: %s: %w", p.host.id, p.name, output, err)
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestThreeHostControllerAcceptance(t *testing.T) {
	local := os.Getenv("TESSERA_ACCEPT_LOCAL") == "1"
	hostsJSON := os.Getenv("TESSERA_ACCEPT_HOSTS")
	if !local && hostsJSON == "" {
		t.Skip("set TESSERA_ACCEPT_HOSTS for three disposable Linux hosts, or TESSERA_ACCEPT_LOCAL=1 for a local Docker rehearsal")
	}
	if local && hostsJSON != "" {
		t.Fatal("choose remote acceptance or local rehearsal, not both")
	}
	hosts := make([]acceptanceHost, 3)
	mode := "local-rehearsal"
	if !local {
		mode = "three-host"
		if err := json.Unmarshal([]byte(hostsJSON), &hosts); err != nil || len(hosts) != 3 {
			t.Fatal("TESSERA_ACCEPT_HOSTS must be three JSON objects with ssh and address fields")
		}
		seen := map[string]bool{}
		for _, host := range hosts {
			if host.SSH == "" || strings.HasPrefix(host.SSH, "-") || strings.ContainsAny(host.SSH, " \t\r\n") || host.Address == "" || strings.ContainsAny(host.Address, " /@,\t\r\n") || seen[host.Address] {
				t.Fatal("each host needs an SSH destination and a distinct reachable address without a port")
			}
			if ip := net.ParseIP(host.Address); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
				t.Fatal("remote controller addresses must be reachable across machines")
			}
			seen[host.Address] = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	results := os.Getenv("TESSERA_ACCEPT_RESULTS")
	if results == "" {
		results = filepath.Join("..", "..", "lab", "results", "controllers-"+time.Now().UTC().Format("20060102T150405Z")+"-"+api.NewID())
	}
	results, err := filepath.Abs(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(results, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s evidence: %s", mode, results)
	report := map[string]any{"mode": mode, "passed": false, "started": time.Now().UTC(), "recovery_ms": map[string]int64{}, "agent_reconnect_ms": map[string]int64{}}
	t.Cleanup(func() {
		report["passed"] = !t.Failed()
		report["finished"] = time.Now().UTC()
		body, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(results, "report.json"), append(body, '\n'), 0o600)
		}
		if err != nil {
			t.Error(err)
		}
	})
	run := func(host acceptanceHost, script string) []byte {
		t.Helper()
		output, err := host.run(ctx, script, nil)
		if err != nil {
			t.Fatalf("host %s: %s: %v", host.id, output, err)
		}
		return output
	}
	port := func() int {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().(*net.TCPAddr).Port
	}
	base := 17468
	if value := os.Getenv("TESSERA_ACCEPT_PORT"); value != "" {
		base, err = strconv.Atoi(value)
		if err != nil || base < 1024 || base > 65533 {
			t.Fatal("TESSERA_ACCEPT_PORT must be between 1024 and 65533")
		}
	}
	routePort := base + 2
	if local {
		routePort = port()
	}
	token := api.NewID() + api.NewID()
	machines := map[string]bool{}
	machineIDs := map[string]string{}
	preserve := map[string]bool{}
	binaries := map[string]string{}
	var peers, urls []string
	for i := range hosts {
		host := &hosts[i]
		host.id = fmt.Sprintf("accept-%d", i)
		goos, arch := runtime.GOOS, runtime.GOARCH
		if !local {
			info := strings.Fields(string(run(*host, "set -eu; uname -s; uname -m; cat /etc/machine-id")))
			if len(info) != 3 || info[0] != "Linux" || machines[info[2]] {
				t.Fatal("acceptance needs three distinct Linux machine IDs")
			}
			machines[info[2]] = true
			machineIDs[host.id] = info[2]
			goos = "linux"
			switch info[1] {
			case "x86_64":
				arch = "amd64"
			case "aarch64", "arm64":
				arch = "arm64"
			default:
				t.Fatalf("unsupported host architecture %s", info[1])
			}
		} else {
			host.Address = "127.0.0.1"
		}
		host.dir = strings.TrimSpace(string(run(*host, "umask 077; mktemp -d /tmp/tessera-accept.XXXXXXXX")))
		if !strings.HasPrefix(host.dir, "/tmp/tessera-accept.") || strings.ContainsAny(host.dir, "\r\n") {
			t.Fatal("invalid temporary host directory")
		}
		owned := *host
		t.Cleanup(func() {
			if preserve[owned.id] {
				t.Logf("preserving %s:%s after incomplete cleanup", owned.SSH, owned.dir)
				return
			}
			cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			if output, err := owned.run(cleanupCtx, "rm -rf -- "+acceptanceQuote(owned.dir), nil); err != nil {
				t.Errorf("remove %s temporary directory: %s: %v", owned.id, output, err)
			}
		})
		key := goos + "-" + arch
		binary := binaries[key]
		if binary == "" {
			binary = filepath.Join(t.TempDir(), "tessera")
			build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
			build.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+arch, "CGO_ENABLED=0")
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build %s: %s: %v", key, output, err)
			}
			binaries[key] = binary
		}
		file, err := os.Open(binary)
		if err != nil {
			t.Fatal(err)
		}
		output, err := host.run(ctx, "umask 077; cat > "+acceptanceQuote(host.dir+"/tessera")+" && chmod 700 "+acceptanceQuote(host.dir+"/tessera"), file)
		file.Close()
		if err != nil {
			t.Fatalf("upload binary: %s: %v", output, err)
		}
		if output, err := host.run(ctx, "umask 077; cat > "+acceptanceQuote(host.dir+"/token"), strings.NewReader(token)); err != nil {
			t.Fatalf("upload token: %s: %v", output, err)
		}
		httpPort, raftPort := base, base+1
		if local {
			httpPort, raftPort = port(), port()
		}
		host.url = "http://" + net.JoinHostPort(host.Address, strconv.Itoa(httpPort))
		host.raft = net.JoinHostPort(host.Address, strconv.Itoa(raftPort))
		peers = append(peers, host.id+"="+host.raft+"@"+host.url)
		urls = append(urls, host.url)
	}
	report["hosts"] = hosts
	report["machine_ids"] = machineIDs
	worker := hosts[0]
	docker := "docker --host " + acceptanceQuote("unix:///var/run/docker.sock")
	if local {
		docker = "docker --host " + acceptanceQuote("unix://"+rt.DockerSock())
	}
	lock := "/tmp/tessera-accept-docker.lock"
	run(worker, "umask 077; mkdir "+acceptanceQuote(lock))
	t.Cleanup(func() {
		if preserve[worker.id] {
			t.Logf("preserving Docker drill lock on %s after incomplete cleanup", worker.id)
			return
		}
		cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if output, err := worker.run(cleanupCtx, "rmdir "+acceptanceQuote(lock), nil); err != nil {
			t.Errorf("release Docker drill lock: %s: %v", output, err)
		}
	})
	run(worker, "set -eu; "+docker+" info >/dev/null; "+docker+" image inspect busybox:latest >/dev/null; test -z \"$("+docker+" ps -a --format '{{.Names}}' | awk '/^tessera_/')\" || { echo 'existing Tessera containers: use a disposable Docker daemon' >&2; exit 1; }; test -z \"$(ps -eo args | awk '/(^|\\/)tessera (agent|up)( |$)/ && !/awk/')\" || { echo 'existing Tessera agent: use a disposable host' >&2; exit 1; }")
	processes := make([]*acceptanceProcess, 3)
	var workerProcess *acceptanceProcess
	ownedContainers := map[string]bool{}
	t.Cleanup(func() {
		if workerProcess != nil {
			if err := workerProcess.stop(); err != nil {
				t.Error(err)
				preserve[worker.id] = true
			}
		}
		for _, process := range processes {
			if process != nil {
				if err := process.stop(); err != nil {
					t.Error(err)
					preserve[process.host.id] = true
				}
			}
		}
		if preserve[worker.id] {
			return
		}
		cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		body, err := worker.run(cleanupCtx, "cat "+acceptanceQuote(worker.dir+"/agent/cache.json"), nil)
		if err == nil {
			var cache struct {
				Assignments []api.Assignment `json:"assignments"`
			}
			if err := json.Unmarshal(body, &cache); err != nil {
				t.Errorf("read disposable assignment identities: %v", err)
			}
			for _, assignment := range cache.Assignments {
				if assignment.App == "accept-steady" && assignment.ID != "" {
					ownedContainers["tessera_"+assignment.ID] = true
				}
			}
		}
		for name := range ownedContainers {
			script := "if " + docker + " container inspect " + acceptanceQuote(name) + " >/dev/null 2>&1; then " + docker + " rm -f " + acceptanceQuote(name) + "; fi"
			if output, err := worker.run(cleanupCtx, script, nil); err != nil {
				t.Errorf("remove disposable workload: %s: %v", output, err)
			}
		}
	})
	start := func(host acceptanceHost, name string, args ...string) *acceptanceProcess {
		t.Helper()
		log, err := os.OpenFile(filepath.Join(results, host.id+"-"+name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		var quoted []string
		for _, arg := range args {
			quoted = append(quoted, acceptanceQuote(arg))
		}
		script := "set -eu; umask 077; echo $$ > " + acceptanceQuote(host.dir+"/"+name+".pid") + "; export TESSERA_DATA=" + acceptanceQuote(host.dir+"/client") + "; export TESSERA_TOKEN=$(cat " + acceptanceQuote(host.dir+"/token") + "); exec " + acceptanceQuote(host.dir+"/tessera") + " " + strings.Join(quoted, " ")
		if local {
			script = "export DOCKER_HOST=" + acceptanceQuote("unix://"+rt.DockerSock()) + "; " + script
		}
		cmd := host.command(ctx, script)
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		process := &acceptanceProcess{host: host, name: name, done: make(chan error, 1)}
		go func() { err := cmd.Wait(); log.Close(); process.done <- err }()
		return process
	}
	startController := func(i int, bootstrap bool) {
		host := hosts[i]
		_, httpPort, _ := net.SplitHostPort(strings.TrimPrefix(host.url, "http://"))
		_, raftPort, _ := net.SplitHostPort(host.raft)
		args := []string{"controller", "--id", host.id, "--listen", ":" + httpPort, "--raft-listen", ":" + raftPort, "--data", host.dir + "/controller", "--peers", strings.Join(peers, ",")}
		if bootstrap {
			args = append(args, "--bootstrap")
		}
		processes[i] = start(host, "controller", args...)
	}
	stopController := func(i int) {
		t.Helper()
		if err := processes[i].stop(); err != nil {
			t.Fatal(err)
		}
		processes[i] = nil
	}
	wait := func(label string, condition func(context.Context) bool) {
		t.Helper()
		deadline, stop := context.WithTimeout(ctx, 45*time.Second)
		defer stop()
		for deadline.Err() == nil {
			if condition(deadline) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s timed out; see %s", label, results)
	}
	status := func(ctx context.Context, i int) (api.ControllerStatus, error) {
		probe, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		return client.New(hosts[i].url, token).ControllerStatus(probe)
	}
	leader := func() int {
		t.Helper()
		found := -1
		wait("writable leader", func(ctx context.Context) bool {
			for i, process := range processes {
				if process != nil {
					if state, err := status(ctx, i); err == nil && state.Writable {
						found = i
						return true
					}
				}
			}
			return false
		})
		return found
	}
	for i := range hosts {
		startController(i, i == 0)
	}
	initialLeader := leader()
	cl := client.New(strings.Join(urls, ","), token)
	workerProcess = start(worker, "agent", "agent", "--id", "accept-worker", "--data", worker.dir+"/agent", "--url", strings.Join(urls, ","), "--runtime", "docker", "--addr", worker.Address)
	manifest := fmt.Sprintf("kind: App\nname: accept-steady\nimage: busybox:latest\ncommand: [sh, -c, 'mkdir -p /www; echo steady > /www/index.html; exec httpd -f -p 80 -h /www']\nports: [{container: 80}]\nhealth: {path: /, port: 80, timeout: 1s, startup_timeout: 30s}\n---\nkind: Route\nname: accept-steady\napp: accept-steady\nport: %d\ntarget_port: 80\n", routePort)
	if err := cl.Apply(ctx, []byte(manifest)); err != nil {
		t.Fatal(err)
	}
	var before api.Assignment
	wait("ready Docker workload", func(ctx context.Context) bool {
		items, err := cl.ListAssignments(ctx)
		if err == nil && len(items) == 1 && items[0].Status == api.StatusRunning && items[0].RuntimeID != "" {
			before = items[0]
			return true
		}
		return false
	})
	report["assignment_before"] = before
	ownedContainers["tessera_"+before.ID] = true
	container := func() string {
		t.Helper()
		return strings.TrimSpace(string(run(worker, docker+" inspect --format '{{.Id}} {{.State.StartedAt}} {{.RestartCount}} {{.State.Running}}' "+acceptanceQuote("tessera_"+before.ID))))
	}
	containerBefore := container()
	report["container_before"] = containerBefore
	checkWorkload := func() {
		t.Helper()
		items, err := cl.ListAssignments(ctx)
		if err != nil || len(items) != 1 || items[0].ID != before.ID || items[0].RuntimeID != before.RuntimeID || items[0].Restarts != before.Restarts || items[0].Status != api.StatusRunning {
			t.Fatalf("workload identity changed: %+v: %v", items, err)
		}
		if actual := container(); actual != containerBefore {
			t.Fatalf("Docker container restarted: before=%s after=%s", containerBefore, actual)
		}
		report["assignment_after"] = items[0]
		report["container_after"] = containerBefore
	}
	gatewayPort := port()
	g := &gateway.Gateway{Client: client.New(strings.Join(urls, ","), token), Route: "accept-steady", Port: gatewayPort}
	if err := g.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	gatewayCtx, stopGateway := context.WithCancel(ctx)
	gatewayDone := make(chan error, 1)
	go func() { gatewayDone <- g.Run(gatewayCtx) }()
	var requests, failures atomic.Int64
	trafficErrors := make(chan string, 1)
	trafficDone := make(chan struct{})
	go func() {
		defer close(trafficDone)
		transport := &http.Transport{DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		httpClient := &http.Client{Timeout: time.Second, Transport: transport}
		for gatewayCtx.Err() == nil {
			req, _ := http.NewRequestWithContext(gatewayCtx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", gatewayPort), nil)
			response, err := httpClient.Do(req)
			if err == nil {
				body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
				response.Body.Close()
				if readErr != nil || response.StatusCode != http.StatusOK || string(body) != "steady\n" {
					err = fmt.Errorf("unexpected HTTP response %d %q: %v", response.StatusCode, body, readErr)
				}
			}
			if gatewayCtx.Err() != nil {
				return
			}
			if err != nil {
				failures.Add(1)
				select {
				case trafficErrors <- err.Error():
				default:
				}
			} else {
				requests.Add(1)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	t.Cleanup(func() {
		stopGateway()
		<-trafficDone
		<-gatewayDone
		report["http_requests"] = requests.Load()
		report["http_failures"] = failures.Load()
		if failures.Load() != 0 {
			first := <-trafficErrors
			report["http_first_error"] = first
			t.Errorf("%d HTTP requests failed at the stable gateway address: %s", failures.Load(), first)
		}
	})
	wait("gateway HTTP", func(context.Context) bool { return requests.Load() >= 10 })
	measure := func(label string, start time.Time) {
		elapsed := time.Since(start).Milliseconds()
		report["recovery_ms"].(map[string]int64)[label] = elapsed
		t.Logf("%s recovered in %dms", label, elapsed)
		node, err := cl.GetNode(ctx, "accept-worker")
		if err != nil {
			t.Fatal(err)
		}
		wait("agent heartbeat after "+label, func(ctx context.Context) bool {
			current, err := cl.GetNode(ctx, "accept-worker")
			return err == nil && current.LastSeen.After(node.LastSeen) && current.Status == api.NodeReady
		})
		report["agent_reconnect_ms"].(map[string]int64)[label] = time.Since(start).Milliseconds()
		checkWorkload()
		if failures.Load() != 0 {
			t.Fatalf("HTTP continuity failed during %s", label)
		}
	}
	started := time.Now()
	stopController(initialLeader)
	next := leader()
	measure("leader_loss", started)
	if err := cl.Apply(ctx, []byte("kind: Config\nname: accept-marker\ndata: {value: committed}\n")); err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	startController(initialLeader, false)
	wait("restarted replica catch-up", func(ctx context.Context) bool {
		current, err := status(ctx, next)
		if err != nil {
			return false
		}
		restarted, err := status(ctx, initialLeader)
		return err == nil && restarted.Epoch == current.Epoch && restarted.AppliedIndex >= current.CommitIndex
	})
	measure("replica_catch_up", started)
	next = leader()
	stopController(next)
	other := (next + 1) % 3
	stopController(other)
	remaining := (next + 2) % 3
	wait("minority loses leader", func(ctx context.Context) bool {
		state, err := status(ctx, remaining)
		return err == nil && !state.Writable && state.LeaderID == ""
	})
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, hosts[remaining].url+"/v1/apply", strings.NewReader("kind: App\nname: accept-steady\nimage: minority-must-not-commit\n"))
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("minority mutation returned %d, want 503", response.StatusCode)
	}
	report["minority_write_status"] = response.StatusCode
	count := requests.Load()
	wait("traffic during quorum loss", func(context.Context) bool { return requests.Load() >= count+50 })
	started = time.Now()
	startController(next, false)
	leader()
	measure("quorum_recovery", started)
	startController(other, false)
	checkApp := func() {
		t.Helper()
		app, err := cl.GetApp(ctx, "accept-steady")
		if err != nil || app.Image != "busybox:latest" || app.Generation != 1 {
			t.Fatalf("minority write changed app: %+v: %v", app, err)
		}
	}
	checkApp()
	for i := range hosts {
		wait("all replicas caught up", func(ctx context.Context) bool {
			current, err := status(ctx, leader())
			if err != nil {
				return false
			}
			state, err := status(ctx, i)
			return err == nil && state.Epoch == current.Epoch && state.AppliedIndex >= current.CommitIndex
		})
	}
	for i := range hosts {
		stopController(i)
	}
	started = time.Now()
	for i := range hosts {
		startController(i, false)
	}
	leader()
	measure("full_restart", started)
	checkApp()
	backupHost := hosts[leader()]
	run(backupHost, acceptanceQuote(backupHost.dir+"/tessera")+" backup --data "+acceptanceQuote(backupHost.dir+"/controller")+" -o "+acceptanceQuote(backupHost.dir+"/backup.db"))
	state, err := cl.ControllerStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range hosts {
		stopController(i)
	}
	started = time.Now()
	run(backupHost, acceptanceQuote(backupHost.dir+"/tessera")+" restore --data "+acceptanceQuote(backupHost.dir+"/restored")+" -f "+acceptanceQuote(backupHost.dir+"/backup.db")+" --epoch "+strconv.FormatUint(state.Epoch+1, 10))
	_, httpPort, _ := net.SplitHostPort(strings.TrimPrefix(backupHost.url, "http://"))
	restoredIndex := -1
	for i, host := range hosts {
		if host.id == backupHost.id {
			restoredIndex = i
		}
	}
	processes[restoredIndex] = start(backupHost, "restored", "controller", "--listen", ":"+httpPort, "--data", backupHost.dir+"/restored")
	wait("offline restore", func(ctx context.Context) bool {
		current, err := status(ctx, restoredIndex)
		return err == nil && current.Writable && current.Epoch > state.Epoch
	})
	measure("offline_restore", started)
	checkApp()
	t.Logf("%d HTTP requests passed; %d failed", requests.Load(), failures.Load())
}
