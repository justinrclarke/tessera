package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"tessera/internal/api"
	"tessera/internal/infra"
	"tessera/internal/store"
)

type InfraAbandoned struct {
	Generation   string        `json:"generation"`
	Action       infra.Action  `json:"action"`
	Desired      infra.Desired `json:"desired"`
	Manifest     string        `json:"manifest,omitempty"`
	OwnershipKey string        `json:"ownership_key"`
}

func pendingInfraKey(state InfraState) string {
	if state.Pending == nil {
		return ""
	}
	if state.Pending.Operation == "confirm" {
		for _, machine := range state.State.Machines {
			if state.Pending.Kind == "machine" && machine.Machine.Name == state.Pending.Name {
				return machine.Resource.Key
			}
		}
		for _, network := range state.State.Networks {
			if state.Pending.Kind == "network" && network.Network.Name == state.Pending.Name {
				return network.Resource.Key
			}
		}
	}
	switch state.Pending.Kind {
	case "machine":
		for _, m := range state.Desired.Machines {
			if m.Name == state.Pending.Name {
				return resourceKey(state.Owner, "machine", m.Name, struct {
					Spec    infra.Machine
					Attempt int
				}{m, state.Attempts["machine:"+m.Name]})
			}
		}
	case "network":
		for _, n := range state.Desired.Networks {
			if n.Name == state.Pending.Name {
				return resourceKey(state.Owner, "network", n.Name, struct {
					Spec    infra.Network
					Attempt int
				}{n, state.Attempts["network:"+n.Name]})
			}
		}
	}
	return resourceKey(state.Owner, "app", state.Pending.Name, state.Generation)
}

func (s *Server) handleInfraAbandon(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := readInfra(s.Store)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if state.Pending == nil {
		http.Error(w, "no pending infrastructure operation", 409)
		return
	}
	key := pendingInfraKey(state)
	action := api.Action{ID: api.NewID(), At: s.now(), Actor: "infra", Kind: "delete", Target: "infra:pending:" + key, Reason: fmt.Sprintf("%s: abandon pending %s/%s; resources with ownership key %s remain recorded for manual review", state.Generation, state.Pending.Kind, state.Pending.Name, key), Result: "proposed"}
	if err := s.Store.AddAction(action); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, action)
}

func (s *Server) abandonInfrastructure(ctx context.Context, state InfraState) (bool, error) {
	actions, err := s.Store.ListActions(10000)
	if err != nil {
		return false, err
	}
	for _, action := range actions {
		if action.Kind != "delete" || action.Result != "confirmed" || !strings.HasPrefix(action.Target, "infra:pending:") {
			continue
		}
		return true, s.commitInfra(ctx, state.Revision, func(st *store.Store, current *InfraState) error {
			if current.Pending == nil || action.Target != "infra:pending:"+pendingInfraKey(*current) {
				action.Result = "error: operation already settled"
				return st.UpdateAction(action)
			}
			current.Abandoned = append(current.Abandoned, InfraAbandoned{Generation: current.Generation, Action: *current.Pending, Desired: current.Desired, Manifest: current.Manifest, OwnershipKey: pendingInfraKey(*current)})
			current.Pending = nil
			current.Phase = "cancelled"
			current.Error = "provider resources are preserved; inspect abandoned ownership keys before cleanup"
			action.Result = "done"
			return st.UpdateAction(action)
		})
	}
	return false, nil
}
