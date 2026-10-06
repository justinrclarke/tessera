package controller

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tessera/internal/api"
	"tessera/internal/ask"
	"tessera/internal/client"
	"tessera/internal/diagnose"
	"tessera/internal/images"
	"tessera/internal/pki"
	"tessera/internal/policy"
	"tessera/internal/survive"
)

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": api.Version})
}

func (s *Server) handleLeader(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	lease := api.Lease{Epoch: s.epoch, LeaderID: s.ID, Expires: s.expires, Leading: s.leading, URL: s.URL}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if !s.leadingNow() {
		http.Error(w, "not leader", http.StatusConflict)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	objs, err := api.DecodeAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	applied, err := s.apply(objs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Reconcile(s.now()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": applied})
}

func (s *Server) apply(objs []api.Object) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var applied []string
	for _, obj := range objs {
		switch {
		case obj.App != nil:
			if err := s.Store.PutApp(*obj.App); err != nil {
				return nil, err
			}
			applied = append(applied, obj.App.Kind+"/"+obj.App.Name)
		case obj.Route != nil:
			if err := s.Store.PutRoute(*obj.Route); err != nil {
				return nil, err
			}
			applied = append(applied, "Route/"+obj.Route.Name)
		case obj.Config != nil:
			if err := s.Store.PutConfig(*obj.Config); err != nil {
				return nil, err
			}
			applied = append(applied, "Config/"+obj.Config.Name)
		case obj.Secret != nil:
			if err := s.Store.PutSecret(*obj.Secret); err != nil {
				return nil, err
			}
			applied = append(applied, "Secret/"+obj.Secret.Name)
		case obj.Policy != nil:
			if err := s.Store.SetPolicy(*obj.Policy); err != nil {
				return nil, err
			}
			applied = append(applied, "Policy")
		}
	}
	return applied, nil
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Store.ListApps()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if apps == nil {
		apps = []api.App{}
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	app, err := s.Store.GetApp(r.PathValue("name"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	asgs, err := s.Store.ListAssignments()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	for _, a := range asgs {
		if a.App != name {
			continue
		}
		if a.Logs == "" {
			continue
		}
		b.WriteString(a.NodeID)
		b.WriteString(": ")
		b.WriteString(a.Logs)
		b.WriteString("\n")
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": b.String()})
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.Store.ListNodes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if nodes == nil {
		nodes = []api.Node{}
	}
	writeJSON(w, http.StatusOK, nodes)
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.GetNode(r.PathValue("id"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.leadingNow() {
		http.Error(w, "not leader", http.StatusConflict)
		return
	}
	var req client.RegisterRequest
	if err := readJSON(w, r, &req); err != nil || req.ID == "" {
		http.Error(w, "bad register", http.StatusBadRequest)
		return
	}
	now := s.now()
	cert, err := pki.Issue(s.ca, req.ID, now, 24*time.Hour)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	n := api.Node{
		ID: req.ID, Addr: req.Addr, Status: api.NodeReady,
		Capacity: req.Capacity, Free: req.Free, Perf: req.Perf, Score: req.Perf.CPU, GPUs: req.GPUs,
		Labels: req.Labels, LastSeen: now, DiskFree: req.DiskFree, DiskTotal: req.DiskTotal,
		CertNotBefore: cert.NotBefore, CertNotAfter: cert.NotAfter, Epoch: s.Epoch(),
	}
	if err := s.Store.PutNode(n); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.Reconcile(now)
	writeJSON(w, http.StatusOK, client.RegisterResponse{
		CertPEM: string(cert.CertPEM), KeyPEM: string(cert.KeyPEM), CAPem: string(s.ca.CertPEM),
		NotBefore: cert.NotBefore, NotAfter: cert.NotAfter, Epoch: s.Epoch(),
	})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req client.HeartbeatRequest
	if err := readJSON(w, r, &req); err != nil {
		http.Error(w, "bad heartbeat", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	now := s.now()
	s.mu.Lock()
	n, err := s.Store.GetNode(id)
	if err != nil {
		s.mu.Unlock()
		http.Error(w, "unknown node", http.StatusNotFound)
		return
	}
	if n.Status != api.NodeCordoned {
		n.Status = api.NodeReady
	}
	n.LastSeen = now
	if req.Addr != "" {
		n.Addr = req.Addr
	}
	if req.Capacity.CPU != 0 || req.Capacity.Memory != 0 {
		n.Capacity = req.Capacity
	}
	n.Free = req.Free
	n.Perf = req.Perf
	n.Score = req.Perf.CPU
	n.GPUs = req.GPUs
	n.DiskFree = req.DiskFree
	n.DiskTotal = req.DiskTotal
	resp := client.HeartbeatResponse{Epoch: s.epoch, LeaderID: s.ID, Expires: s.expires, Leading: s.leading}
	if n.DiskTotal > 0 && n.DiskFree*10 < n.DiskTotal {
		resp.Prune = true
	}
	if pki.NeedsRenewal(n.CertNotBefore, n.CertNotAfter, now) {
		if cert, err := pki.Issue(s.ca, id, now, 24*time.Hour); err == nil {
			n.CertNotBefore = cert.NotBefore
			n.CertNotAfter = cert.NotAfter
			resp.CertPEM = string(cert.CertPEM)
			resp.KeyPEM = string(cert.KeyPEM)
			resp.NotBefore = cert.NotBefore
			resp.NotAfter = cert.NotAfter
			_ = s.recordLocked(now, "renew", id, "cert half-life", "done")
		}
	}
	if err := s.Store.PutNode(n); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if snap, _, err := s.Store.LatestSnapshot(); err == nil {
		resp.SnapshotIndex = snap.Index
	}
	resp.Command = s.pendingCommandLocked(id)
	s.mu.Unlock()
	_ = s.Reconcile(now)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAssignments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	waitMs, _ := strconv.ParseInt(r.URL.Query().Get("wait_ms"), 10, 64)
	if waitMs < 0 {
		waitMs = 0
	}
	if waitMs > 60000 {
		waitMs = 60000
	}
	s.mu.Lock()
	if s.rev <= since && waitMs > 0 {
		ch := make(chan struct{}, 1)
		s.waiters = append(s.waiters, ch)
		s.mu.Unlock()
		timer := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
		canceled := false
		select {
		case <-ch:
		case <-timer.C:
		case <-r.Context().Done():
			canceled = true
		}
		timer.Stop()
		s.mu.Lock()
		for i, waiter := range s.waiters {
			if waiter == ch {
				s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
				break
			}
		}
		if canceled {
			s.mu.Unlock()
			http.Error(w, "canceled", http.StatusRequestTimeout)
			return
		}
	}
	page := client.Page{Rev: s.rev, Assignments: s.assignmentsForLocked(id)}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) assignmentsForLocked(nodeID string) []api.Assignment {
	asgs, err := s.Store.ListAssignments()
	if err != nil {
		return nil
	}
	var out []api.Assignment
	for _, a := range asgs {
		if a.NodeID == nodeID && api.Active(a.Status) {
			out = append(out, a)
		}
	}
	if out == nil {
		out = []api.Assignment{}
	}
	return out
}

func (s *Server) handleListAssignments(w http.ResponseWriter, r *http.Request) {
	asgs, err := s.Store.ListAssignments()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range asgs {
		asgs[i].Env = nil
	}
	if asgs == nil {
		asgs = []api.Assignment{}
	}
	writeJSON(w, http.StatusOK, asgs)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var rep client.StatusReport
	if err := readJSON(w, r, &rep); err != nil {
		http.Error(w, "bad status", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	now := s.now()
	s.mu.Lock()
	asg, err := s.Store.GetAssignment(id)
	if err != nil {
		s.mu.Unlock()
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	asg.Status = rep.Status
	asg.Reason = rep.Reason
	asg.Restarts = rep.Restarts
	asg.RuntimeID = rep.RuntimeID
	asg.HostPort = rep.HostPort
	if rep.Logs != "" {
		asg.Logs = tail(rep.Logs, 8192)
	}
	asg.Updated = now
	if err := s.Store.PutAssignment(asg); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rep.Status == api.StatusRunning {
		_ = s.Store.MarkHealthy(asg.App, asg.Generation)
	}
	s.mu.Unlock()
	_ = s.Reconcile(now)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.leadingNow() {
		http.Error(w, "not leader", http.StatusConflict)
		return
	}
	id := r.PathValue("id")
	now := s.now()
	s.mu.Lock()
	a, err := s.Store.GetAction(id)
	if err != nil {
		s.mu.Unlock()
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if a.Result != "proposed" {
		s.mu.Unlock()
		http.Error(w, "not proposed", http.StatusConflict)
		return
	}
	if a.Kind != "wipe" && a.Kind != "reimage" && a.Kind != "delete" {
		s.mu.Unlock()
		http.Error(w, "cannot confirm", http.StatusBadRequest)
		return
	}
	switch a.Kind {
	case "delete":
		if _, nodeErr := s.Store.GetNode(a.Target); nodeErr == nil {
			a.Result = "confirmed"
		} else {
			if err = s.applyDeleteLocked(a.Target); err != nil {
				s.mu.Unlock()
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			a.Result = "done"
		}
	case "wipe", "reimage":
		if _, err = s.Store.GetNode(a.Target); err != nil {
			s.mu.Unlock()
			http.Error(w, "unknown node", http.StatusNotFound)
			return
		}
		a.Result = "confirmed"
	}
	if err := s.Store.UpdateAction(a); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	kind := a.Kind
	result := a.Result
	s.mu.Unlock()
	if kind == "delete" && result == "done" {
		_ = s.Reconcile(now)
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": result})
}

func (s *Server) handleActionResult(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Result string `json:"result"`
	}
	if err := readJSON(w, r, &req); err != nil || req.Result == "" {
		http.Error(w, "bad result", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	now := s.now()
	s.mu.Lock()
	a, err := s.Store.GetAction(id)
	if err != nil {
		s.mu.Unlock()
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if a.Result != "confirmed" {
		s.mu.Unlock()
		http.Error(w, "not confirmed", http.StatusConflict)
		return
	}
	if (a.Kind == "wipe" || a.Kind == "delete") && req.Result == "done" {
		if err := s.Store.DeleteNode(a.Target); err != nil {
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	a.Result = req.Result
	if err := s.Store.UpdateAction(a); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Unlock()
	_ = s.Reconcile(now)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) applyDeleteLocked(target string) error {
	if _, err := s.Store.GetApp(target); err == nil {
		return s.Store.DeleteApp(target)
	}
	return fmt.Errorf("unknown target %s", target)
}

func (s *Server) pendingCommandLocked(nodeID string) *client.NodeCommand {
	a, err := s.Store.PendingNodeAction(nodeID)
	if err != nil {
		return nil
	}
	return &client.NodeCommand{ID: a.ID, Kind: a.Kind}
}

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		http.Error(w, "ref required", http.StatusBadRequest)
		return
	}
	dir := s.DataDir
	if dir == "" {
		http.Error(w, "no data dir", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodHead:
		if !images.Has(dir, ref) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		f, err := images.Open(dir, ref)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = io.Copy(w, f)
	case http.MethodPut:
		if !s.leadingNow() {
			http.Error(w, "not leader", http.StatusConflict)
			return
		}
		if err := images.Save(dir, ref, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleActions(w http.ResponseWriter, r *http.Request) {
	actions, err := s.Store.ListActions(100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if actions == nil {
		actions = []api.Action{}
	}
	writeJSON(w, http.StatusOK, actions)
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := s.Store.ListRoutes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if routes == nil {
		routes = []api.Route{}
	}
	writeJSON(w, http.StatusOK, routes)
}

func (s *Server) handleDiagnose(w http.ResponseWriter, r *http.Request) {
	findings, err := s.findings()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, findings)
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string `json:"question"`
	}
	_ = readJSON(w, r, &req)
	text, err := s.explain(req.Question)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	body, sig, err := s.Store.RawSnapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var snap api.Snapshot
	if body != "" {
		if err := json.Unmarshal([]byte(body), &snap); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, client.SignedSnapshot{Snapshot: snap, Body: body, Sig: sig})
}

func (s *Server) handleElection(w http.ResponseWriter, r *http.Request) {
	var c survive.Claim
	if err := readJSON(w, r, &c); err != nil {
		http.Error(w, "bad claim", http.StatusBadRequest)
		return
	}
	stepped := s.Observe(c)
	writeJSON(w, http.StatusOK, map[string]any{"stepped_down": stepped, "leading": s.Leading()})
}

func (s *Server) handleAct(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind   string `json:"kind"`
		Target string `json:"target"`
		Reason string `json:"reason"`
	}
	if err := readJSON(w, r, &req); err != nil {
		http.Error(w, "bad act", http.StatusBadRequest)
		return
	}
	pol, err := s.Store.Policy()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := s.now()
	result := "proposed"
	if policy.Decide(pol, req.Kind) == "auto" {
		s.mu.Lock()
		err = s.executeLocked(diagnose.Finding{Action: req.Kind, Target: req.Target, Reason: req.Reason}, now)
		s.mu.Unlock()
		if err != nil {
			result = "error: " + err.Error()
		} else {
			result = "done"
		}
	}
	s.mu.Lock()
	err = s.recordLocked(now, req.Kind, req.Target, req.Reason, result)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": result})
}

func (s *Server) findings() ([]diagnose.Finding, error) {
	apps, err := s.Store.ListApps()
	if err != nil {
		return nil, err
	}
	nodes, err := s.Store.ListNodes()
	if err != nil {
		return nil, err
	}
	asgs, err := s.Store.ListAssignments()
	if err != nil {
		return nil, err
	}
	pol, err := s.Store.Policy()
	if err != nil {
		return nil, err
	}
	return diagnose.Scan(apps, nodes, asgs, pol.MaxRestarts, s.now()), nil
}

func (s *Server) explain(question string) (string, error) {
	apps, err := s.Store.ListApps()
	if err != nil {
		return "", err
	}
	nodes, err := s.Store.ListNodes()
	if err != nil {
		return "", err
	}
	asgs, err := s.Store.ListAssignments()
	if err != nil {
		return "", err
	}
	actions, err := s.Store.ListActions(20)
	if err != nil {
		return "", err
	}
	findings, err := s.findings()
	if err != nil {
		return "", err
	}
	return ask.Explain(ask.View{Question: question, Apps: apps, Nodes: nodes, Assignments: asgs, Actions: actions, Findings: findings}), nil
}

func (s *Server) leadingNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leading
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func jsonMarshalStd(v any) ([]byte, error) {
	return json.Marshal(v)
}
