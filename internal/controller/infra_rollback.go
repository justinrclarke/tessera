package controller

import (
	"io"
	"net/http"
	"strings"
)

func (s *Server) handleInfraRollback(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Generation string `json:"generation"`
	}
	if err := readJSON(w, r, &request); err != nil {
		http.Error(w, "invalid rollback request", 400)
		return
	}
	state, err := readInfra(s.Store)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if state.Pending != nil {
		http.Error(w, "cannot roll back during a provider operation; resolve or finish the recorded operation first", 409)
		return
	}
	if request.Generation == "" {
		if len(state.History) == 0 {
			http.Error(w, "no previous infrastructure generation", 409)
			return
		}
		request.Generation = state.History[len(state.History)-1]
	}
	body, err := s.Store.Meta("infra:history:" + request.Generation)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if body == "" {
		http.Error(w, "unknown infrastructure generation", 404)
		return
	}
	r.Body = io.NopCloser(strings.NewReader(body))
	s.handleInfraApply(w, r)
}
