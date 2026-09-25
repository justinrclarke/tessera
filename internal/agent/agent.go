package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tessera/internal/api"
	"tessera/internal/bench"
	"tessera/internal/client"
	"tessera/internal/controller"
	"tessera/internal/discover"
	"tessera/internal/host"
	"tessera/internal/runtime"
	"tessera/internal/store"
	"tessera/internal/survive"
)

type Agent struct {
	ID          string
	DataDir     string
	Token       string
	URL         string
	Addr        string
	Runtime     runtime.Runtime
	Client      *client.Client
	MaxRestarts int
	Now         func() time.Time
	LostAt      time.Time
	Others      int
	PeerIDs     []string
	Reexec      func() error

	maxEpoch  uint64
	snapIndex uint64
	restarts  map[string]int
	rev       int64
	promoted  *controller.Server
	perfAt    time.Time
	perfScore api.Perf
}

type diskCache struct {
	NodeID      string           `json:"node_id"`
	Assignments []api.Assignment `json:"assignments"`
	Snapshot    api.Snapshot     `json:"snapshot"`
	SnapBody    string           `json:"snap_body"`
	Sig         string           `json:"sig"`
	Epoch       uint64           `json:"epoch"`
	Perf        api.Perf         `json:"perf,omitempty"`
	PerfAt      time.Time        `json:"perf_at,omitempty"`
}

var ErrHalted = errors.New("halted")

func (a *Agent) Run(ctx context.Context) error {
	a.init()
	_ = a.restore(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.Tick(ctx); err != nil {
			if errors.Is(err, ErrHalted) {
				return err
			}
			a.noteLoss()
			_, _ = a.maybePromote(ctx)
			if !sleep(ctx, time.Second) {
				return ctx.Err()
			}
			continue
		}
		a.LostAt = time.Time{}
		page, err := a.Client.WaitAssignments(ctx, a.ID, a.rev, time.Second)
		if err != nil {
			a.noteLoss()
			_, _ = a.maybePromote(ctx)
			continue
		}
		a.rev = page.Rev
		if err := a.converge(ctx, page.Assignments, true); err != nil {
			return err
		}
	}
}

func (a *Agent) Tick(ctx context.Context) error {
	a.init()
	if a.Client == nil {
		return a.restore(ctx)
	}
	if err := a.ensureRegistered(ctx); err != nil {
		return err
	}
	hb, err := a.Client.Heartbeat(ctx, a.ID, a.reportHost())
	if err != nil {
		return err
	}
	if hb.Epoch > a.maxEpoch {
		a.maxEpoch = hb.Epoch
	}
	if hb.Command != nil {
		return a.execCommand(ctx, hb.Command)
	}
	if !hb.Leading {
		return fmt.Errorf("leader not leading")
	}
	if survive.Expired(survive.Lease{Epoch: hb.Epoch, LeaderID: hb.LeaderID, Expires: hb.Expires}, a.now()) {
		return fmt.Errorf("lease expired")
	}
	if hb.Prune && a.Runtime != nil {
		_ = a.Runtime.Prune(ctx)
	}
	if hb.CertPEM != "" {
		_ = a.saveCert(hb.CertPEM, hb.KeyPEM)
	}
	if hb.SnapshotIndex > a.snapIndex {
		_ = a.fetchSnapshot(ctx)
	}
	page, err := a.Client.WaitAssignments(ctx, a.ID, a.rev, 0)
	if err != nil {
		return err
	}
	a.rev = page.Rev
	return a.converge(ctx, page.Assignments, true)
}

func (a *Agent) SetEpoch(epoch uint64) { a.maxEpoch = epoch }

func (a *Agent) SaveForTest(asgs []api.Assignment) error {
	a.init()
	return a.saveCache(asgs)
}

func (a *Agent) FetchForTest(ctx context.Context) error { return a.fetchSnapshot(ctx) }

func (a *Agent) PromoteForTest(ctx context.Context) (bool, error) { return a.maybePromote(ctx) }

func (a *Agent) ConvergeForTest(ctx context.Context, desired []api.Assignment, live bool) error {
	a.init()
	return a.converge(ctx, desired, live)
}

func (a *Agent) maybePromote(ctx context.Context) (bool, error) {
	a.init()
	if a.promoted != nil {
		return true, nil
	}
	if a.Client != nil {
		if lease, err := a.Client.Leader(ctx); err == nil && lease.Leading && !survive.Expired(survive.Lease{Epoch: lease.Epoch, LeaderID: lease.LeaderID, Expires: lease.Expires}, a.now()) {
			return false, nil
		}
	}
	snap, err := a.loadSnapshot()
	if err != nil || snap.Index == 0 {
		return false, err
	}
	epoch := a.maxEpoch
	if snap.Epoch > epoch {
		epoch = snap.Epoch
	}
	d := survive.Decide(survive.Lease{Epoch: epoch, LeaderID: "down", Expires: a.now().Add(-time.Second)}, a.LostAt, a.now(), snap.Policy.SoloAfter(), a.Others)
	if !d.Promote {
		return false, nil
	}
	if !survive.ShouldLead(a.ID, a.PeerIDs) {
		return false, nil
	}
	if err := a.startPromoted(ctx, snap, d.Epoch); err != nil {
		return false, err
	}
	return true, nil
}

func (a *Agent) startPromoted(ctx context.Context, snap api.Snapshot, epoch uint64) error {
	dir := filepath.Join(a.DataDir, "promoted")
	st, err := store.Open(filepath.Join(dir, "tessera.db"))
	if err != nil {
		return err
	}
	if err := st.LoadSnapshot(snap, epoch); err != nil {
		return err
	}
	srv, err := controller.New(st, controller.Config{DataDir: dir, Token: a.Token, ID: a.ID, Epoch: epoch})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	a.promoted = srv
	a.maxEpoch = epoch
	a.URL = "http://" + ln.Addr().String()
	a.Client = client.New(a.URL, a.Token)
	go func() { _ = srv.Serve(ctx, ln) }()
	return nil
}

func (a *Agent) restore(ctx context.Context) error {
	a.init()
	c, err := a.readCache()
	if err != nil {
		return nil
	}
	if c.NodeID != "" {
		a.ID = c.NodeID
	}
	a.maxEpoch = c.Epoch
	if c.Snapshot.Index > 0 {
		a.snapIndex = c.Snapshot.Index
	}
	if a.Runtime == nil {
		return nil
	}
	return a.converge(ctx, c.Assignments, false)
}

func (a *Agent) converge(ctx context.Context, desired []api.Assignment, live bool) error {
	if a.Runtime == nil {
		return a.saveCache(desired)
	}
	have, err := a.Runtime.List(ctx)
	if err != nil {
		return err
	}
	byName := map[string]runtime.Container{}
	for _, c := range have {
		byName[c.Name] = c
	}
	want := map[string]bool{}
	var keep []api.Assignment
	for _, d := range desired {
		if !api.Active(d.Status) {
			continue
		}
		if !survive.AcceptAssignment(a.maxEpoch, d.Epoch) {
			continue
		}
		name := "tessera_" + d.ID
		want[d.ID] = true
		keep = append(keep, d)
		c, ok := byName[name]
		if ok && c.Running {
			a.report(ctx, d, api.StatusRunning, "", c)
			continue
		}
		if ok && !c.Running {
			if d.Kind == api.KindJob && c.ExitCode == 0 && !c.OOM {
				a.report(ctx, d, api.StatusSucceeded, "", c)
				continue
			}
			a.restarts[d.ID]++
			reason := c.Reason
			if c.OOM {
				reason = "oom"
			}
			if reason == "" {
				reason = "crash"
			}
			if a.restarts[d.ID] >= a.max() {
				a.report(ctx, d, api.StatusFailed, reason, c)
				continue
			}
			if err := a.Runtime.Stop(ctx, c.ID); err != nil {
				a.report(ctx, d, api.StatusFailed, err.Error(), c)
				continue
			}
		}
		a.ensureImage(ctx, d.Image)
		started, err := a.Runtime.Start(ctx, runtime.Spec{
			Name: name, Image: d.Image, Command: d.Command, Env: d.Env, Ports: d.Ports, Resources: d.Resources, GPUs: d.GPUs,
		})
		if err != nil {
			reason := err.Error()
			if se, ok := err.(*runtime.StartError); ok {
				reason = se.Reason
			}
			a.report(ctx, d, api.StatusFailed, reason, runtime.Container{})
			continue
		}
		if started.Name == "" {
			started.Name = name
		}
		a.report(ctx, d, api.StatusRunning, "", started)
		a.publishImage(ctx, d.Image)
	}
	if live {
		for name, c := range byName {
			id := strings.TrimPrefix(name, "tessera_")
			if !want[id] {
				_ = a.Runtime.Stop(ctx, c.ID)
			}
		}
	}
	return a.saveCache(keep)
}

func (a *Agent) report(ctx context.Context, d api.Assignment, status, reason string, c runtime.Container) {
	if a.Client == nil {
		return
	}
	logs := ""
	if c.ID != "" && a.Runtime != nil {
		logs, _ = a.Runtime.Logs(ctx, c.ID)
	}
	_ = a.Client.Report(ctx, d.ID, client.StatusReport{
		Status: status, Reason: reason, Restarts: a.restarts[d.ID], RuntimeID: c.ID, HostPort: c.HostPort, Logs: logs,
	})
}

func (a *Agent) ensureRegistered(ctx context.Context) error {
	a.init()
	if a.ID == "" {
		if c, err := a.readCache(); err == nil && c.NodeID != "" {
			a.ID = c.NodeID
		} else {
			a.ID = "node-" + api.NewID()
		}
	}
	_, err := a.Client.Register(ctx, client.RegisterRequest{
		ID: a.ID, Addr: a.addr(), Capacity: a.capacity(), Free: a.free(), Perf: a.perf(), DiskFree: a.diskFree(), DiskTotal: a.diskTotal(),
	})
	return err
}

func (a *Agent) fetchSnapshot(ctx context.Context) error {
	signed, err := a.Client.Snapshot(ctx)
	if err != nil {
		return err
	}
	if signed.Snapshot.Index == 0 {
		return nil
	}
	if signed.Body != "" && signed.Sig != "" && !survive.Verify(a.Token, []byte(signed.Body), signed.Sig) {
		return fmt.Errorf("snapshot signature mismatch")
	}
	a.snapIndex = signed.Snapshot.Index
	if signed.Snapshot.Epoch > a.maxEpoch {
		a.maxEpoch = signed.Snapshot.Epoch
	}
	c, _ := a.readCache()
	c.Snapshot = signed.Snapshot
	c.SnapBody = signed.Body
	c.Sig = signed.Sig
	c.Epoch = a.maxEpoch
	c.NodeID = a.ID
	return a.writeCache(c)
}

func (a *Agent) loadSnapshot() (api.Snapshot, error) {
	c, err := a.readCache()
	if err != nil {
		return api.Snapshot{}, err
	}
	if c.Sig != "" && c.SnapBody != "" && a.Token != "" && !survive.Verify(a.Token, []byte(c.SnapBody), c.Sig) {
		return api.Snapshot{}, fmt.Errorf("snapshot signature mismatch")
	}
	return c.Snapshot, nil
}

func (a *Agent) saveCache(desired []api.Assignment) error {
	c, _ := a.readCache()
	c.NodeID = a.ID
	c.Assignments = desired
	c.Epoch = a.maxEpoch
	return a.writeCache(c)
}

func (a *Agent) readCache() (diskCache, error) {
	var c diskCache
	b, err := os.ReadFile(a.cachePath())
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

func (a *Agent) writeCache(c diskCache) error {
	if err := os.MkdirAll(a.DataDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.cachePath(), b, 0o600)
}

func (a *Agent) saveCert(cert, key string) error {
	if err := os.MkdirAll(a.DataDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(a.DataDir, "node.crt"), []byte(cert), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(a.DataDir, "node.key"), []byte(key), 0o600)
}

func (a *Agent) cachePath() string { return filepath.Join(a.DataDir, "cache.json") }

func (a *Agent) init() {
	if a.restarts == nil {
		a.restarts = map[string]int{}
	}
	if a.Now == nil {
		a.Now = time.Now
	}
	if a.MaxRestarts == 0 {
		a.MaxRestarts = 3
	}
	if a.DataDir == "" {
		a.DataDir = filepath.Join(os.TempDir(), "tessera-agent")
	}
}

func (a *Agent) noteLoss() {
	if a.LostAt.IsZero() {
		a.LostAt = a.now()
	}
}

func (a *Agent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Agent) max() int {
	if a.MaxRestarts == 0 {
		return 3
	}
	return a.MaxRestarts
}

func (a *Agent) reportHost() client.HeartbeatRequest {
	cap, free, diskTotal, diskFree := host.Resources(a.DataDir)
	return client.HeartbeatRequest{
		Addr: a.addr(), Capacity: cap, Free: free, Perf: a.perf(), DiskFree: diskFree, DiskTotal: diskTotal,
	}
}

func (a *Agent) capacity() api.Resources {
	cap, _, _, _ := host.Resources(a.DataDir)
	return cap
}

func (a *Agent) free() api.Resources {
	_, free, _, _ := host.Resources(a.DataDir)
	return free
}

func (a *Agent) diskFree() int64 {
	_, _, _, free := host.Resources(a.DataDir)
	return free
}

func (a *Agent) diskTotal() int64 {
	_, _, total, _ := host.Resources(a.DataDir)
	return total
}

func (a *Agent) perf() api.Perf {
	if a.perfAt.IsZero() {
		if c, err := a.readCache(); err == nil && !c.PerfAt.IsZero() {
			a.perfScore = c.Perf
			a.perfAt = c.PerfAt
		}
	}
	if a.perfAt.IsZero() || a.now().Sub(a.perfAt) >= time.Hour {
		a.perfScore = bench.Quick(a.DataDir)
		a.perfAt = a.now()
		c, _ := a.readCache()
		c.Perf = a.perfScore
		c.PerfAt = a.perfAt
		if c.NodeID == "" {
			c.NodeID = a.ID
		}
		_ = a.writeCache(c)
	}
	return a.perfScore
}

func (a *Agent) addr() string {
	if a.Addr != "" {
		return a.Addr
	}
	ips := discover.LocalIPs()
	if len(ips) > 0 {
		return ips[0].String()
	}
	return "127.0.0.1"
}

func (a *Agent) ensureImage(ctx context.Context, ref string) {
	if a.Runtime == nil || ref == "" {
		return
	}
	if has, err := a.Runtime.HasImage(ctx, ref); err == nil && has {
		return
	}
	if a.Client == nil {
		return
	}
	rc, err := a.Client.GetImage(ctx, ref)
	if err != nil {
		return
	}
	defer rc.Close()
	_ = a.Runtime.ImportImage(ctx, ref, rc)
}

func (a *Agent) publishImage(ctx context.Context, ref string) {
	if a.Client == nil || a.Runtime == nil || ref == "" {
		return
	}
	if a.Client.HasImage(ctx, ref) {
		return
	}
	rc, err := a.Runtime.ExportImage(ctx, ref)
	if err != nil {
		return
	}
	defer rc.Close()
	_ = a.Client.PutImage(ctx, ref, rc)
}

func (a *Agent) execCommand(ctx context.Context, cmd *client.NodeCommand) error {
	if cmd == nil {
		return nil
	}
	switch cmd.Kind {
	case "wipe", "delete":
		err := a.clearLocal(ctx, false)
		finishErr := a.finish(ctx, cmd.ID, err)
		if err != nil {
			return err
		}
		if finishErr != nil {
			return finishErr
		}
		return ErrHalted
	case "reimage":
		err := a.clearLocal(ctx, true)
		finishErr := a.finish(ctx, cmd.ID, err)
		if err != nil {
			return err
		}
		if finishErr != nil {
			return finishErr
		}
		if a.Reexec != nil {
			return a.Reexec()
		}
		return reexec()
	default:
		return nil
	}
}

func (a *Agent) finish(ctx context.Context, id string, err error) error {
	if a.Client == nil || id == "" {
		return nil
	}
	result := "done"
	if err != nil {
		result = "error: " + err.Error()
	}
	return a.Client.FinishCommand(ctx, id, result)
}

func (a *Agent) clearLocal(ctx context.Context, keepID bool) error {
	if a.Runtime != nil {
		items, err := a.Runtime.List(ctx)
		if err != nil {
			return err
		}
		for _, c := range items {
			if strings.HasPrefix(c.Name, "tessera_") || strings.HasPrefix(c.ID, "tessera_") {
				if err := a.Runtime.Stop(ctx, c.ID); err != nil {
					return err
				}
			}
		}
		if err := a.Runtime.Prune(ctx); err != nil {
			return err
		}
	}
	if a.DataDir != "" {
		entries, err := os.ReadDir(a.DataDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, e := range entries {
			if err := os.RemoveAll(filepath.Join(a.DataDir, e.Name())); err != nil {
				return err
			}
		}
	}
	if keepID && a.ID != "" {
		return a.writeCache(diskCache{NodeID: a.ID, Epoch: a.maxEpoch})
	}
	return nil
}

func reexec() error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(bin, os.Args, os.Environ())
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
