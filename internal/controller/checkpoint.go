package controller

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"tessera/internal/api"
	"tessera/internal/policy"
	"tessera/internal/schedule"
	"tessera/internal/store"
)

type checkpointMove struct {
	ID         string                `json:"id"`
	Generation int64                 `json:"generation"`
	Phase      string                `json:"phase"`
	Request    api.CheckpointRequest `json:"request"`
	Old        []string              `json:"old"`
	New        []string              `json:"new"`
}

func (s *Server) checkpointMoves(apps []api.App, nodes []api.Node, assignments []api.Assignment, pol api.Policy, now time.Time) (map[string]bool, error) {
	pinned := map[string]bool{}
	for _, app := range apps {
		var move checkpointMove
		body, err := s.Store.Meta("checkpoint:" + app.Name)
		if err != nil {
			return nil, err
		}
		if body != "" {
			if err := json.Unmarshal([]byte(body), &move); err != nil {
				return nil, err
			}
		}
		if move.Phase == "saving" || move.Phase == "stopping" || move.Phase == "starting" {
			pinned[app.Name] = true
			previous := move.Phase
			if err := s.advanceCheckpoint(app, &move, now); err != nil {
				return nil, err
			}
			if previous != move.Phase {
				if err := s.writeSnapshotLocked(now); err != nil {
					return nil, err
				}
			}
			if move.Phase == "failed" || move.Phase == "done" {
				delete(pinned, app.Name)
			}
			continue
		}
		if !policy.AllowsAuto(pol, "move") || app.Checkpoint == nil || (!s.lastMove[app.Name].IsZero() && now.Sub(s.lastMove[app.Name]) < pol.Cooldown()) {
			continue
		}
		targets := schedule.CheckpointTargets(schedule.Input{Apps: apps, Nodes: nodes, Assignments: assignments, MinGain: pol.MoveMinGain}, app)
		if len(targets) == 0 {
			continue
		}
		id := api.NewID()
		timeout := 2 * time.Minute
		if app.Checkpoint.Timeout != "" {
			timeout, err = time.ParseDuration(app.Checkpoint.Timeout)
			if err != nil {
				return nil, err
			}
		}
		move = checkpointMove{ID: id, Generation: app.Generation, Phase: "saving", Request: api.CheckpointRequest{ID: id, Directory: app.Checkpoint.Directory, URI: "file://" + filepath.Join(app.Checkpoint.Directory, "generations", id), Deadline: now.Add(timeout)}}
		var replacements []api.Assignment
		for _, target := range targets {
			next, err := s.assignmentFrom(target, now)
			if err != nil {
				return nil, err
			}
			next.Status, next.Checkpoint = api.StatusReserved, &move.Request
			move.New = append(move.New, next.ID)
			replacements = append(replacements, next)
		}
		if err := s.Store.Transaction(func(st *store.Store) error {
			for _, old := range assignments {
				if old.App != app.Name || !api.Active(old.Status) {
					continue
				}
				old.Checkpoint, old.CheckpointDone = &move.Request, ""
				move.Old = append(move.Old, old.ID)
				if err := st.PutAssignment(old); err != nil {
					return err
				}
			}
			byID := map[string]api.Assignment{}
			for _, a := range assignments {
				byID[a.ID] = a
			}
			sort.Slice(move.Old, func(i, j int) bool {
				a, _ := strconv.Atoi(byID[move.Old[i]].Env["TESSERA_WORKER_RANK"])
				b, _ := strconv.Atoi(byID[move.Old[j]].Env["TESSERA_WORKER_RANK"])
				return a < b
			})
			for _, next := range replacements {
				if err := st.PutAssignment(next); err != nil {
					return err
				}
			}
			encoded, _ := json.Marshal(move)
			if err := st.SetMeta("checkpoint:"+app.Name, string(encoded)); err != nil {
				return err
			}
			return st.SetMeta("move:"+app.Name, strconv.FormatInt(now.Unix(), 10))
		}); err != nil {
			return nil, err
		}
		s.lastMove[app.Name] = now
		pinned[app.Name] = true
		s.rev++
		s.notifyLocked()
		if err := s.writeSnapshotLocked(now); err != nil {
			return nil, err
		}
	}
	return pinned, nil
}

func (s *Server) advanceCheckpoint(app api.App, move *checkpointMove, now time.Time) error {
	previous := move.Phase
	return s.Store.Transaction(func(st *store.Store) error {
		if move.Phase == "saving" {
			ready, failed := true, app.Generation != move.Generation || !now.Before(move.Request.Deadline)
			for _, id := range move.Old {
				a, err := st.GetAssignment(id)
				if err != nil {
					return err
				}
				ready = ready && a.Status == api.StatusRunning && a.CheckpointDone == move.ID
				failed = failed || a.Status != api.StatusRunning
			}
			for _, id := range move.New {
				a, err := st.GetAssignment(id)
				if err != nil {
					return err
				}
				n, err := st.GetNode(a.NodeID)
				failed = failed || err != nil || n.Status != api.NodeReady
			}
			if failed {
				for _, id := range move.New {
					a, err := st.GetAssignment(id)
					if err != nil {
						return err
					}
					a.Status = api.StatusStopped
					if err := st.PutAssignment(a); err != nil {
						return err
					}
				}
				for _, id := range move.Old {
					a, err := st.GetAssignment(id)
					if err != nil {
						return err
					}
					a.Checkpoint = nil
					a.CheckpointDone = ""
					if err := st.PutAssignment(a); err != nil {
						return err
					}
				}
				move.Phase = "failed"
			} else if ready {
				for _, id := range move.Old {
					a, err := st.GetAssignment(id)
					if err != nil {
						return err
					}
					a.Status = api.StatusStopping
					if err := st.PutAssignment(a); err != nil {
						return err
					}
				}
				move.Phase = "stopping"
			}
		} else if move.Phase == "stopping" {
			ready := true
			for _, id := range move.Old {
				a, err := st.GetAssignment(id)
				if err != nil {
					return err
				}
				ready = ready && a.Status == api.StatusStopped
			}
			if ready {
				for rank, id := range move.New {
					a, err := st.GetAssignment(id)
					if err != nil {
						return err
					}
					a.Status = api.StatusPending
					a.Checkpoint = nil
					if a.Env == nil {
						a.Env = map[string]string{}
					}
					a.Env["TESSERA_CHECKPOINT_URI"], a.Env["TESSERA_CHECKPOINT_GENERATION"], a.Env["TESSERA_WORKER_RANK"] = move.Request.URI, move.ID, fmt.Sprint(rank)
					old, err := st.GetAssignment(move.Old[rank])
					if err != nil {
						return err
					}
					a.Env["TESSERA_CHECKPOINT_SOURCE_ASSIGNMENT"] = old.ID
					a.Env["TESSERA_CHECKPOINT_PROGRESS"] = strconv.FormatInt(old.CheckpointProgress, 10)
					if err := st.PutAssignment(a); err != nil {
						return err
					}
				}
				move.Phase = "starting"
			}
		} else if move.Phase == "starting" {
			ready := true
			for _, id := range move.New {
				a, err := st.GetAssignment(id)
				if err != nil {
					return err
				}
				ready = ready && a.Status == api.StatusRunning
			}
			if ready {
				move.Phase = "done"
			}
		}
		if move.Phase == previous {
			return nil
		}
		encoded, _ := json.Marshal(move)
		if err := st.SetMeta("checkpoint:"+app.Name, string(encoded)); err != nil {
			return err
		}
		s.rev++
		s.notifyLocked()
		return nil
	})
}
