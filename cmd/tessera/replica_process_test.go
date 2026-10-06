package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tessera/internal/agent"
	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/runtime"
)

type replicaProcess struct {
	cmd  *exec.Cmd
	done chan error
	log  string
}

type countedRuntime struct {
	*runtime.Fake
	starts int
	stops  int
}

func (r *countedRuntime) Start(ctx context.Context, spec runtime.Spec) (runtime.Container, error) {
	r.starts++
	return r.Fake.Start(ctx, spec)
}

func (r *countedRuntime) Stop(ctx context.Context, id string) error {
	r.stops++
	return r.Fake.Stop(ctx, id)
}

func TestControllerProcessesSurviveAbruptLeaderAndQuorumLoss(t *testing.T) {
	root := t.TempDir()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var listeners []net.Listener
	var addresses []string
	for i := 0; i < 6; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, ln)
		addresses = append(addresses, ln.Addr().String())
	}
	var peers, urls []string
	ids := []string{"a", "b", "c"}
	for i, id := range ids {
		urls = append(urls, "http://"+addresses[i])
		peers = append(peers, id+"="+addresses[i+3]+"@"+urls[i])
	}
	for _, ln := range listeners {
		ln.Close()
	}
	processes := make([]*replicaProcess, 3)
	stop := func(i int) {
		if process := processes[i]; process != nil {
			_ = process.cmd.Process.Kill()
			<-process.done
			processes[i] = nil
		}
	}
	t.Cleanup(func() {
		for i := range processes {
			stop(i)
		}
	})
	start := func(i int, bootstrap bool) {
		t.Helper()
		args := []string{"--id", ids[i], "--listen", addresses[i], "--raft-listen", addresses[i+3], "--data", filepath.Join(root, ids[i]), "--token", "process-test-token", "--peers", strings.Join(peers, ",")}
		if bootstrap {
			args = append(args, "--bootstrap")
		}
		body, _ := json.Marshal(args)
		cmd := exec.CommandContext(ctx, bin, "-test.run=^TestReplicaControllerProcessHelper$")
		cmd.Env = append(os.Environ(), "TESSERA_REPLICA_PROCESS_ARGS="+string(body))
		logPath := filepath.Join(root, ids[i]+".log")
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { err := cmd.Wait(); log.Close(); done <- err }()
		processes[i] = &replicaProcess{cmd: cmd, done: done, log: logPath}
	}
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			if condition() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		for _, process := range processes {
			if process != nil {
				body, _ := os.ReadFile(process.log)
				t.Log(string(body))
			}
		}
		t.Fatal("controller process condition timed out")
	}
	status := func(i int) (api.ControllerStatus, error) {
		probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		return client.New(urls[i], "process-test-token").ControllerStatus(probeCtx)
	}
	for i := range processes {
		start(i, i == 0)
	}
	leader := -1
	wait(func() bool {
		for i := range processes {
			if s, err := status(i); err == nil && s.Writable {
				leader = i
				return true
			}
		}
		return false
	})
	cl := client.New(strings.Join(urls, ","), "process-test-token")
	if err := cl.Apply(ctx, []byte("kind: App\nname: steady\nimage: steady\n")); err != nil {
		t.Fatal(err)
	}
	rtm := &countedRuntime{Fake: runtime.NewFake()}
	ag := &agent.Agent{ID: "worker", DataDir: filepath.Join(root, "agent"), Client: cl, Token: cl.Token, Runtime: rtm}
	if err := ag.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := cl.ListAssignments(ctx)
	if err != nil || len(before) != 1 || before[0].Status != api.StatusRunning {
		t.Fatalf("initial workload: %+v %v", before, err)
	}
	stop(leader)
	if err := ag.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := cl.ListAssignments(ctx)
	if err != nil || len(after) != 1 || after[0].ID != before[0].ID || after[0].Epoch <= before[0].Epoch || rtm.starts != 1 || rtm.stops != 0 {
		t.Fatalf("abrupt leader loss restarted work: %+v starts=%d stops=%d err=%v", after, rtm.starts, rtm.stops, err)
	}
	next := -1
	wait(func() bool {
		for i, process := range processes {
			if process != nil {
				if s, err := status(i); err == nil && s.Writable {
					next = i
					return true
				}
			}
		}
		return false
	})
	stop(next)
	failedCtx, failedCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	err = cl.Apply(failedCtx, []byte("kind: App\nname: steady\nimage: minority\n"))
	failedCancel()
	if err == nil {
		t.Fatal("one remaining process committed a mutation")
	}
	start(leader, false)
	wait(func() bool {
		for i, process := range processes {
			if process != nil {
				if s, err := status(i); err == nil && s.Writable {
					return true
				}
			}
		}
		return false
	})
	if err := ag.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	app, err := cl.GetApp(ctx, "steady")
	if err != nil || app.Image != "steady" || rtm.starts != 1 || rtm.stops != 0 {
		t.Fatalf("quorum recovery changed running work: %+v starts=%d stops=%d err=%v", app, rtm.starts, rtm.stops, err)
	}
}

func TestReplicaControllerProcessHelper(t *testing.T) {
	body := os.Getenv("TESSERA_REPLICA_PROCESS_ARGS")
	if body == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(body), &args); err != nil {
		t.Fatal(err)
	}
	if err := cmdController(args); err != nil {
		t.Fatal(err)
	}
}
