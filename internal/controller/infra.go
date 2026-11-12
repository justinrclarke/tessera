package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"tessera/internal/api"
	"tessera/internal/cloud"
	"tessera/internal/infra"
	"tessera/internal/store"
)

type InfraState struct {
	Abandoned    []InfraAbandoned `json:"abandoned,omitempty"`
	History      []string         `json:"history,omitempty"`
	DiscoveredAt time.Time        `json:"discovered_at,omitempty"`
	Attempts     map[string]int   `json:"attempts,omitempty"`
	RetryAfter   time.Time        `json:"retry_after,omitempty"`
	Failures     int              `json:"failures,omitempty"`
	Revision     uint64           `json:"revision"`
	Owner        string           `json:"owner"`
	Generation   string           `json:"generation"`
	Manifest     string           `json:"manifest,omitempty"`
	Desired      infra.Desired    `json:"desired"`
	State        infra.State      `json:"state"`
	Pending      *infra.Action    `json:"pending,omitempty"`
	Phase        string           `json:"phase"`
	Error        string           `json:"error,omitempty"`
}

func readInfra(st *store.Store) (InfraState, error) {
	var state InfraState
	raw, err := st.Meta("infra:state")
	if err != nil {
		return state, err
	}
	if raw != "" {
		err = json.Unmarshal([]byte(raw), &state)
	}
	return state, err
}

func (s *Server) handleInfraApply(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	desired, err := infra.Parse(body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := readInfra(s.Store)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	plan, err := infra.Plan(desired, state.State)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if state.Pending != nil && state.Generation != plan.Generation {
		http.Error(w, "an operation is pending; inspect infra status before changing intent", 409)
		return
	}
	if state.Owner != "" && !reflect.DeepEqual(state.Desired.Providers, desired.Providers) && (len(state.State.Machines) > 0 || len(state.State.Networks) > 0) {
		http.Error(w, "provider scope changes require a separate cluster intent", 409)
		return
	}
	if state.Generation == plan.Generation {
		if state.Phase == "cancelled" {
			state.Phase = "pending"
			state.Revision++
			encoded, _ := json.Marshal(state)
			if err := s.Store.SetMeta("infra:state", string(encoded)); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		writeJSON(w, 200, map[string]string{"generation": state.Generation})
		return
	}
	if state.Owner == "" {
		state.Owner = api.NewID()
	}
	if state.Generation != "" {
		previous := state.Manifest
		if previous == "" {
			encoded, err := infraManifest(state.Desired)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			previous = string(encoded)
		}
		if err := s.Store.SetMeta("infra:history:"+state.Generation, previous); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		state.History = append(state.History, state.Generation)
		if len(state.History) > 20 {
			old := state.History[0]
			state.History = state.History[1:]
			keep := false
			for _, generation := range state.History {
				keep = keep || generation == old
			}
			if !keep {
				if err := s.Store.DeleteMeta("infra:history:" + old); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
			}
		}
	}
	state.Desired, state.Generation, state.Phase, state.Error = desired, plan.Generation, "pending", ""
	state.Manifest = string(body)
	state.RetryAfter, state.Failures = time.Time{}, 0
	state.Revision++
	encoded, _ := json.Marshal(state)
	if err := s.Store.SetMeta("infra:state", string(encoded)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.writeSnapshotLocked(s.now()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, map[string]string{"generation": state.Generation})
}

func (s *Server) handleInfraStatus(w http.ResponseWriter, r *http.Request) {
	state, err := readInfra(s.Store)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, state)
}

func (s *Server) commitInfra(ctx context.Context, revision uint64, fn func(*store.Store, *InfraState) error) error {
	apply := func(st *store.Store) error {
		state, err := readInfra(st)
		if err != nil {
			return err
		}
		if state.Revision != revision {
			return fmt.Errorf("infrastructure state changed; retry")
		}
		if err := fn(st, &state); err != nil {
			return err
		}
		state.Revision++
		encoded, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if err := st.SetMeta("infra:state", string(encoded)); err != nil {
			return err
		}
		worker := &Server{Store: st, Token: s.Token, ID: s.ID, epoch: s.epoch, controllers: s.controllerURLs()}
		return worker.writeSnapshotLocked(s.now())
	}
	if s.replica != nil {
		return s.propose(ctx, s.now(), func(worker *Server) error { return apply(worker.Store) })
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.leading {
		return fmt.Errorf("not leader")
	}
	return s.Store.Transaction(apply)
}

func resourceKey(owner, kind, name string, spec any) string {
	body, _ := json.Marshal(spec)
	sum := sha256.Sum256(append([]byte(owner+":"+kind+":"+name+":"), body...))
	return hex.EncodeToString(sum[:])[:32]
}

func shellAtom(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func (s *Server) joinScript(nodeID string) string {
	urls := s.controllerURLs()
	if len(urls) == 0 {
		urls = []string{s.URL}
	}
	return "#!/bin/sh\nset -eu\numask 077\ncommand -v tessera >/dev/null\nmkdir -p /etc/tessera /var/lib/tessera\nprintf '%s\\n' " + shellAtom("export TESSERA_TOKEN="+shellAtom(s.makeJoinToken(nodeID, 24*time.Hour))) + " " + shellAtom("export TESSERA_URL="+shellAtom(strings.Join(urls, ","))) + " > /etc/tessera/environment\ncat > /etc/systemd/system/tessera-agent.service <<'UNIT'\n[Unit]\nAfter=network-online.target docker.service\nWants=network-online.target\n[Service]\nExecStart=/bin/sh -c '. /etc/tessera/environment; exec /usr/local/bin/tessera agent --restricted --data /var/lib/tessera/agent --id " + nodeID + " --url \"$TESSERA_URL\"'\nRestart=on-failure\n[Install]\nWantedBy=multi-user.target\nUNIT\ncp \"$(command -v tessera)\" /usr/local/bin/tessera.installing\nchmod 755 /usr/local/bin/tessera.installing\nmv /usr/local/bin/tessera.installing /usr/local/bin/tessera\nsystemctl daemon-reload\nsystemctl enable --now tessera-agent.service\n"
}

func (s *Server) provider(name string, settings cloud.Settings) cloud.Provider {
	if s.CloudFactory != nil {
		return s.CloudFactory(name, settings)
	}
	return CloudCLI{Provider: name, Settings: settings}
}

func (s *Server) ReconcileInfrastructure(ctx context.Context) error {
	if !s.cloudMu.TryLock() {
		return nil
	}
	defer s.cloudMu.Unlock()
	if !s.leadingNow() {
		return nil
	}
	state, err := readInfra(s.Store)
	if err != nil || state.Owner == "" {
		return err
	}
	if handled, err := s.abandonInfrastructure(ctx, state); handled || err != nil {
		return err
	}
	if state.Phase == "cancelled" {
		return nil
	}
	if s.now().Before(state.RetryAfter) {
		return nil
	}
	if state.Pending == nil && state.Phase == "ready" && s.now().Sub(state.DiscoveredAt) >= 30*time.Second {
		err := s.discoverInfrastructure(ctx, state)
		if err != nil {
			_ = s.commitInfra(ctx, state.Revision, func(_ *store.Store, current *InfraState) error {
				current.Error = err.Error()
				current.Phase = "error"
				current.RetryAfter = s.now().Add(30 * time.Second)
				return nil
			})
		}
		return err
	}
	plan, err := infra.Plan(state.Desired, state.State)
	if err != nil {
		return err
	}
	if len(plan.Actions) == 0 && state.Pending == nil {
		if state.Phase == "ready" {
			return nil
		}
		return s.commitInfra(ctx, state.Revision, func(_ *store.Store, current *InfraState) error {
			current.Phase, current.Error = "ready", ""
			return nil
		})
	}
	var action infra.Action
	if state.Pending != nil {
		action = *state.Pending
	} else {
		action = plan.Actions[0]
		if action.Operation == "confirm" {
			target := "infra:" + action.Kind + ":" + action.Name
			reason := state.Generation + ":" + action.Reason
			actions, err := s.Store.ListActions(10000)
			if err != nil {
				return err
			}
			confirmed := false
			proposed := false
			for _, a := range actions {
				if a.Kind == "delete" && a.Target == target && a.Reason == reason {
					confirmed = confirmed || a.Result == "confirmed"
					proposed = proposed || a.Result == "proposed"
				}
			}
			if !confirmed {
				if proposed && state.Phase == "confirm" {
					return nil
				}
				return s.commitInfra(ctx, state.Revision, func(st *store.Store, current *InfraState) error {
					current.Phase = "confirm"
					if !proposed {
						return st.AddAction(api.Action{ID: api.NewID(), At: s.now(), Actor: "infra", Kind: "delete", Target: target, Reason: reason, Result: "proposed"})
					}
					return nil
				})
			}
		}
		if err := s.commitInfra(ctx, state.Revision, func(_ *store.Store, current *InfraState) error {
			current.Pending = &action
			current.Phase = "applying"
			current.Error = ""
			return nil
		}); err != nil {
			return err
		}
		state.Revision++
	}
	err = s.performInfra(ctx, state, action)
	if err != nil {
		_ = s.commitInfra(ctx, state.Revision, func(_ *store.Store, current *InfraState) error {
			current.Phase = "error"
			current.Error = err.Error()
			current.Failures++
			delay := time.Second * time.Duration(1<<min(current.Failures, 8))
			if strings.HasPrefix(err.Error(), "waiting for machine ") {
				current.Phase = "waiting"
				delay = 2 * time.Second
			}
			current.RetryAfter = s.now().Add(delay)
			return nil
		})
	}
	return err
}

func (s *Server) performInfra(ctx context.Context, state InfraState, action infra.Action) error {
	next := state.State
	if action.Operation == "confirm" {
		return s.removeInfra(ctx, state, action)
	}
	switch action.Kind {
	case "network":
		var spec infra.Network
		for _, n := range state.Desired.Networks {
			if n.Name == action.Name {
				spec = n
			}
		}
		key := resourceKey(state.Owner, "network", spec.Name, struct {
			Spec    infra.Network
			Attempt int
		}{spec, state.Attempts["network:"+spec.Name]})
		settings := state.Desired.Providers[spec.Provider]
		settings.Region = spec.Region
		resource, err := s.provider(spec.Provider, settings).CreateNetwork(ctx, cloud.NetworkSpec{Name: "ts-" + key[:20], Region: spec.Region, CIDR: spec.CIDR, Key: key})
		if err != nil {
			return err
		}
		next.Networks = append(next.Networks, infra.ExistingNetwork{Network: spec, Owned: true, Resource: resource})
	case "machine":
		var spec infra.Machine
		for _, m := range state.Desired.Machines {
			if m.Name == action.Name {
				spec = m
			}
		}
		settings := state.Desired.Providers[spec.Provider]
		settings.Region = spec.Region
		provider := s.provider(spec.Provider, settings)
		key := resourceKey(state.Owner, "machine", spec.Name, struct {
			Spec    infra.Machine
			Attempt int
		}{spec, state.Attempts["machine:"+spec.Name]})
		nodeID := "cloud-" + key
		var resource cloud.Resource
		if spec.Adopt != "" {
			items, err := provider.Discover(ctx)
			if err != nil {
				return err
			}
			for _, item := range items {
				if item.ID == spec.Adopt {
					resource = cloud.Resource{ID: item.ID, Name: item.Name, Region: spec.Region}
				}
			}
			if resource.ID == "" {
				return fmt.Errorf("adopted instance %s not found", spec.Adopt)
			}
			nodeID = spec.NodeID
		} else {
			request := cloud.MachineSpec{Name: "ts-" + key[:20], Region: spec.Region, Type: spec.Type, Image: spec.Image, Key: key, UserData: s.joinScript(nodeID)}
			for _, network := range next.Networks {
				if network.Network.Name == spec.Network {
					request.Network, request.Subnet = network.Resource.ID, network.Resource.Subnet
					if spec.Provider == "gcp" || spec.Provider == "azure" {
						request.Network = network.Resource.Name
					}
				}
			}
			var err error
			resource, err = provider.Create(ctx, request)
			if err != nil {
				return err
			}
		}
		next.Machines = append(next.Machines, infra.ExistingMachine{Machine: spec, Resource: resource, NodeID: nodeID, Owned: spec.Adopt == ""})
	case "app":
		for _, machine := range next.Machines {
			node, err := s.Store.GetNode(machine.NodeID)
			if err != nil || node.Status != api.NodeReady {
				return fmt.Errorf("waiting for machine %s to join as node %s", machine.Machine.Name, machine.NodeID)
			}
		}
		var app api.App
		for _, a := range state.Desired.Apps {
			if a.Name == action.Name {
				app = a
			}
		}
		desiredApp := app
		return s.commitInfra(ctx, state.Revision, func(st *store.Store, current *InfraState) error {
			_, existingErr := st.GetApp(app.Name)
			if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
				return existingErr
			}
			worker := &Server{Store: st}
			if _, err := worker.applyObjects([]api.Object{{Kind: app.Kind, App: &app}}); err != nil {
				return err
			}
			found := false
			for i, a := range current.State.Apps {
				if a.App.Name == app.Name {
					current.State.Apps[i].App = desiredApp
					found = true
				}
			}
			if !found {
				current.State.Apps = append(current.State.Apps, infra.ExistingApp{App: desiredApp, Owned: existingErr != nil})
			}
			current.Pending = nil
			current.Error = ""
			current.RetryAfter, current.Failures = time.Time{}, 0
			return nil
		})
	default:
		return fmt.Errorf("unknown infrastructure operation")
	}
	return s.commitInfra(ctx, state.Revision, func(_ *store.Store, current *InfraState) error {
		current.State = next
		current.Pending = nil
		current.Error = ""
		current.RetryAfter, current.Failures = time.Time{}, 0
		return nil
	})
}

func (s *Server) removeInfra(ctx context.Context, state InfraState, action infra.Action) error {
	next := state.State
	switch action.Kind {
	case "machine":
		for i, m := range next.Machines {
			if m.Machine.Name != action.Name {
				continue
			}
			if !m.Owned {
				return fmt.Errorf("adopted machines cannot be removed or replaced")
			}
			asgs, err := s.Store.ListAssignments()
			if err != nil {
				return err
			}
			for _, a := range asgs {
				if a.NodeID == m.NodeID && api.Active(a.Status) {
					return fmt.Errorf("machine %s still has active workloads; move or stop them before confirmed removal", action.Name)
				}
			}
			settings := state.Desired.Providers[m.Machine.Provider]
			settings.Region = m.Machine.Region
			if err := s.provider(m.Machine.Provider, settings).Stop(ctx, m.Resource); err != nil {
				return err
			}
			next.Machines = append(next.Machines[:i], next.Machines[i+1:]...)
			break
		}
	case "network":
		for i, n := range next.Networks {
			if n.Network.Name != action.Name {
				continue
			}
			if !n.Owned {
				return fmt.Errorf("adopted networks cannot be removed or replaced")
			}
			for _, m := range next.Machines {
				if m.Machine.Network == n.Network.Name {
					return fmt.Errorf("network still has machines")
				}
			}
			settings := state.Desired.Providers[n.Network.Provider]
			settings.Region = n.Network.Region
			if err := s.provider(n.Network.Provider, settings).DeleteNetwork(ctx, n.Resource); err != nil {
				return err
			}
			next.Networks = append(next.Networks[:i], next.Networks[i+1:]...)
			break
		}
	case "app":
	default:
		return fmt.Errorf("unknown infrastructure removal")
	}
	return s.commitInfra(ctx, state.Revision, func(st *store.Store, current *InfraState) error {
		if action.Kind == "machine" || action.Kind == "network" {
			if current.Attempts == nil {
				current.Attempts = map[string]int{}
			}
			current.Attempts[action.Kind+":"+action.Name]++
		}
		if action.Kind == "app" {
			for i, a := range next.Apps {
				if a.App.Name == action.Name {
					if !a.Owned {
						return fmt.Errorf("adopted app cannot be removed")
					}
					if err := st.DeleteApp(action.Name); err != nil {
						return err
					}
					next.Apps = append(next.Apps[:i], next.Apps[i+1:]...)
					break
				}
			}
		}
		current.State = next
		current.Pending = nil
		current.Error = ""
		current.RetryAfter, current.Failures = time.Time{}, 0
		actions, err := st.ListActions(10000)
		if err != nil {
			return err
		}
		for _, a := range actions {
			if a.Target == "infra:"+action.Kind+":"+action.Name && a.Reason == state.Generation+":"+action.Reason && a.Result == "confirmed" {
				a.Result = "done"
				if err := st.UpdateAction(a); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Server) triggerInfrastructure(ctx context.Context) {
	go func() {
		operation, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		_ = s.ReconcileInfrastructure(operation)
	}()
}
