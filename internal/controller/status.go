package controller

import (
	"context"
	"net/http"

	"tessera/internal/api"

	"github.com/hashicorp/raft"
)

func (s *Server) handleControllerStatus(w http.ResponseWriter, r *http.Request) {
	status := api.ControllerStatus{ID: s.ID, URL: s.URL, Epoch: s.Epoch(), Role: "standalone", LeaderID: s.ID, Writable: s.Leading()}
	if replica := s.replica; replica != nil {
		status.Role = replica.raft.State().String()
		_, leaderID := replica.raft.LeaderWithID()
		status.LeaderID = string(leaderID)
		status.CommitIndex = replica.raft.CommitIndex()
		status.AppliedIndex = replica.raft.AppliedIndex()
		status.Writable = false
		if replica.failed.Load() {
			status.Error = "replicated state failed to apply; inspect the store before restarting"
		} else if replica.raft.State() == raft.Leader {
			ctx, cancel := context.WithTimeout(r.Context(), replica.timeout)
			defer cancel()
			if err := awaitFuture(ctx, replica.raft.VerifyLeader()); err != nil {
				status.Error = "controller has no writable majority"
			} else {
				status.Writable = replica.ready()
			}
		}
	}
	writeJSON(w, http.StatusOK, status)
}
