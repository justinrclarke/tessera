package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"tessera/internal/api"
	"tessera/internal/diagnose"
	"tessera/internal/policy"
	"tessera/internal/proxy"
	"tessera/internal/schedule"
	"tessera/internal/survive"
)

func (s *Server) Reconcile(now time.Time) error {
	if s.replica != nil {
		return s.propose(context.Background(), now, func(worker *Server) error { return worker.Reconcile(now) })
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.leading {
		return nil
	}
	pol := s.mustPolicy()
	s.expires = now.Add(pol.Lease())
	if err := s.expireNodesLocked(now, pol); err != nil {
		return err
	}
	if err := s.healLocked(now, pol); err != nil {
		return err
	}
	return s.scheduleLocked(now, pol)
}

func (s *Server) expireNodesLocked(now time.Time, pol api.Policy) error {
	nodes, err := s.Store.ListNodes()
	if err != nil {
		return err
	}
	for _, n := range nodes {
		next := n.Status
		if n.Status == api.NodeCordoned {
			if !n.CordonedAt.IsZero() && now.Sub(n.CordonedAt) >= pol.Cooldown() && now.Sub(n.LastSeen) < pol.Suspect() {
				n.Status = api.NodeReady
				n.CordonedAt = time.Time{}
				if err := s.recordLocked(now, "uncordon", n.ID, "cordon cooldown elapsed", "done"); err != nil {
					return err
				}
			}
		} else if !n.LastSeen.IsZero() {
			age := now.Sub(n.LastSeen)
			switch {
			case age > pol.Dead():
				next = api.NodeDead
			case age > pol.Suspect():
				next = api.NodeSuspect
			default:
				next = api.NodeReady
			}
			n.Status = next
		}
		if err := s.Store.PutNode(n); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) healLocked(now time.Time, pol api.Policy) error {
	apps, err := s.Store.ListApps()
	if err != nil {
		return err
	}
	nodes, err := s.Store.ListNodes()
	if err != nil {
		return err
	}
	asgs, err := s.Store.ListAssignments()
	if err != nil {
		return err
	}
	findings := diagnose.Scan(apps, nodes, asgs, pol.MaxRestarts, now)
	for _, f := range findings {
		reason := f.Class + ":" + f.Reason
		if f.Action == "rollback" {
			if app, err := s.Store.GetApp(f.Target); err == nil {
				reason += fmt.Sprintf(" (generation %d)", app.Generation)
			}
		}
		has, err := s.Store.HasAction(f.Action, f.Target, reason)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		result := "proposed"
		if policy.Decide(pol, f.Action) == "auto" {
			if err := s.executeLocked(f, now); err != nil {
				result = "error: " + err.Error()
			} else {
				result = "done"
			}
		}
		if err := s.recordLocked(now, f.Action, f.Target, reason, result); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) executeLocked(f diagnose.Finding, now time.Time) error {
	switch f.Action {
	case "rollback":
		_, err := s.Store.Rollback(f.Target)
		return err
	case "cordon":
		n, err := s.Store.GetNode(f.Target)
		if err != nil {
			return err
		}
		n.Status = api.NodeCordoned
		n.CordonedAt = now
		return s.Store.PutNode(n)
	case "wipe", "reimage", "delete":
		return fmt.Errorf("destructive actions are not executed")
	default:
		return nil
	}
}

func (s *Server) scheduleLocked(now time.Time, pol api.Policy) error {
	apps, err := s.Store.ListApps()
	if err != nil {
		return err
	}
	nodes, err := s.Store.ListNodes()
	if err != nil {
		return err
	}
	asgs, err := s.Store.ListAssignments()
	if err != nil {
		return err
	}
	plan := schedule.Plan(schedule.Input{
		Apps:        apps,
		Nodes:       nodes,
		Assignments: asgs,
		MinGain:     pol.MoveMinGain,
		Cooldown:    pol.Cooldown(),
		MaxRestarts: pol.MaxRestarts,
		LastMove:    s.lastMove,
		Now:         now,
		AllowMove:   policy.AllowsAuto(pol, "move"),
	})
	changed := false
	for _, id := range plan.Stop {
		asg, err := s.Store.GetAssignment(id)
		if err != nil {
			continue
		}
		if asg.Status == api.StatusStopped {
			continue
		}
		asg.Status = api.StatusStopped
		asg.Updated = now
		if err := s.Store.PutAssignment(asg); err != nil {
			return err
		}
		changed = true
	}
	for _, p := range plan.Place {
		asg, err := s.assignmentFrom(p, now)
		if err != nil {
			return err
		}
		if err := s.Store.PutAssignment(asg); err != nil {
			return err
		}
		asgs = append(asgs, asg)
		changed = true
		if p.Replaces != "" {
			old, err := s.Store.GetAssignment(p.Replaces)
			if err == nil && old.Generation != p.Generation {
				_ = s.recordLocked(now, "rollout", p.App, "starting replacement generation", "done")
			} else {
				s.lastMove[p.App] = now
				_ = s.Store.SetMeta("move:"+p.App, strconv.FormatInt(now.Unix(), 10))
				_ = s.recordLocked(now, "move", p.App, "faster node "+p.NodeID, "done")
			}
		}
	}
	if changed {
		s.rev++
		if err := s.writeSnapshotLocked(now); err != nil {
			return err
		}
		s.notifyLocked()
	}
	s.syncProxyLocked()
	return nil
}

func (s *Server) assignmentFrom(p schedule.Placement, now time.Time) (api.Assignment, error) {
	app, err := s.Store.GetApp(p.App)
	if err != nil {
		return api.Assignment{}, err
	}
	env, err := s.envFor(app)
	if err != nil {
		return api.Assignment{}, err
	}
	return api.Assignment{
		ID:         api.NewID(),
		App:        app.Name,
		NodeID:     p.NodeID,
		Image:      app.Image,
		Generation: p.Generation,
		Epoch:      s.epoch,
		Status:     api.StatusPending,
		Replaces:   p.Replaces,
		Env:        env,
		Command:    app.Command,
		Ports:      app.Ports,
		Resources:  app.Resources,
		GPUs:       app.GPUs,
		GPUDevices: p.GPUDevices,
		Kind:       app.Kind,
		Health:     app.Health,
		Updated:    now,
	}, nil
}

func (s *Server) envFor(app api.App) (map[string]string, error) {
	if app.EnvResolved {
		return app.ReleaseEnv, nil
	}
	return s.resolveEnv(app)
}

func (s *Server) resolveEnv(app api.App) (map[string]string, error) {
	env := map[string]string{}
	for k, v := range app.Env {
		env[k] = v
	}
	for _, name := range app.Configs {
		c, err := s.Store.GetConfig(name)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", name, err)
		}
		for k, v := range c.Data {
			env[k] = v
		}
	}
	for _, name := range app.Secrets {
		sec, err := s.Store.GetSecret(name)
		if err != nil {
			return nil, fmt.Errorf("secret %s: %w", name, err)
		}
		for k, v := range sec.Data {
			env[k] = v
		}
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}

func (s *Server) writeSnapshotLocked(now time.Time) error {
	v, _ := s.Store.Meta("snapshot_index")
	idx, _ := strconv.ParseUint(v, 10, 64)
	idx++
	apps, err := s.Store.ListApps()
	if err != nil {
		return err
	}
	asgs, err := s.Store.ListAssignments()
	if err != nil {
		return err
	}
	for i := range asgs {
		asgs[i].Logs = ""
	}
	routes, _ := s.Store.ListRoutes()
	cfgs, _ := s.Store.ListConfigs()
	secs, _ := s.Store.ListSecrets()
	pol, err := s.Store.Policy()
	if err != nil {
		return err
	}
	snap := api.Snapshot{
		Index: idx, Epoch: s.epoch, LeaderID: s.ID, Taken: now,
		Apps: apps, Assignments: asgs, Policy: pol, Routes: routes, Configs: cfgs, Secrets: secs,
		Controllers: s.controllerURLs(),
	}
	raw, err := jsonMarshal(snap)
	if err != nil {
		return err
	}
	sig := survive.Sign(s.Token, raw)
	if err := s.Store.AppendSnapshotRaw(idx, string(raw), sig); err != nil {
		return err
	}
	return s.Store.SetMeta("snapshot_index", strconv.FormatUint(idx, 10))
}

func (s *Server) syncProxyLocked() {
	if s.proxy == nil {
		return
	}
	routes, err := s.Store.ListRoutes()
	if err != nil {
		return
	}
	asgs, _ := s.Store.ListAssignments()
	nodes, _ := s.Store.ListNodes()
	names := map[string]bool{}
	for _, r := range routes {
		names[r.Name] = true
		_ = s.proxy.SetBackends(r.Name, r.Port, proxy.Backends(r, asgs, nodes))
	}
	s.proxy.RemoveExcept(names)
}

func (s *Server) recordLocked(now time.Time, kind, target, reason, result string) error {
	return s.Store.AddAction(api.Action{
		ID: api.NewID(), At: now, Actor: "playbook", Kind: kind, Target: target, Reason: reason, Result: result,
	})
}

func (s *Server) notifyLocked() {
	for _, ch := range s.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.waiters = nil
}

func jsonMarshal(v any) ([]byte, error) {
	return jsonMarshalStd(v)
}
