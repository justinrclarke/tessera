package controller

import (
	"net/http"

	"tessera/internal/api"
	"tessera/internal/proxy"
)

func (s *Server) handleRouteBackends(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	routes, err := s.Store.ListRoutes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, route := range routes {
		if route.Name != r.PathValue("name") {
			continue
		}
		assignments, err := s.Store.ListAssignments()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		nodes, err := s.Store.ListNodes()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, api.RouteBackends{Epoch: s.epoch, Backends: proxy.Backends(route, assignments, nodes)})
		return
	}
	http.Error(w, "route not found", http.StatusNotFound)
}
