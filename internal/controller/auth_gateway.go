package controller

import (
	"encoding/json"
	"net/http"
	"strings"

	"tessera/internal/api"
)

type gatewayCredential struct {
	Hash  string `json:"hash"`
	Route string `json:"route"`
}

func (s *Server) handleGatewayToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Route string `json:"route"`
	}
	if err := readJSON(w, r, &req); err != nil || req.Route == "" || strings.ContainsAny(req.Route, "\x00\r\n") {
		http.Error(w, "route required", 400)
		return
	}
	id := api.NewID()
	token := "tessera-gateway." + id + "." + api.NewID() + api.NewID() + api.NewID() + api.NewID()
	body, _ := json.Marshal(gatewayCredential{Hash: tokenHash(token), Route: req.Route})
	if err := s.Store.SetMeta("auth:gateway:"+id, string(body)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.mu.Lock()
	err := s.writeSnapshotLocked(s.now())
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, map[string]string{"token": token, "id": id})
}
