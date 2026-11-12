package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"tessera/internal/api"
	"tessera/internal/config"
	"tessera/internal/store"
)

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type joinClaim struct {
	Node     string `json:"node"`
	Revision string `json:"revision,omitempty"`
	Expires  int64  `json:"expires"`
	Nonce    string `json:"nonce"`
}

var scopedNodeID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func (s *Server) makeJoinToken(node string, ttl time.Duration) string {
	revision, _ := s.Store.Meta("auth:join:" + node)
	body, _ := json.Marshal(joinClaim{Node: node, Revision: revision, Expires: s.now().Add(ttl).Unix(), Nonce: api.NewID()})
	encoded := base64.RawURLEncoding.EncodeToString(body)
	return "tessera-join." + encoded + "." + s.joinSignature(encoded)
}

func (s *Server) adminHash() string {
	hash, err := s.Store.Meta("auth:admin")
	if err != nil {
		return ""
	}
	if hash == "" {
		return tokenHash(s.Token)
	}
	return hash
}

func (s *Server) ClientToken() string {
	expected := s.adminHash()
	if expected == "" {
		return ""
	}
	if tokenHash(s.Token) == expected {
		return s.Token
	}
	if saved, err := config.Load(s.DataDir); err == nil && tokenHash(saved.Token) == expected {
		return saved.Token
	}
	return ""
}
func (s *Server) joinSignature(body string) string {
	mac := hmac.New(sha256.New, []byte(s.adminHash()))
	mac.Write([]byte("join:" + body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("X-Tessera-Node")
		r.Header.Del("X-Tessera-Join")
		if r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		admin := s.adminHash()
		if admin == "" {
			http.Error(w, "credential store unavailable", http.StatusServiceUnavailable)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(tokenHash(token)), []byte(admin)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(token, "tessera-join.") {
			parts := strings.Split(token, ".")
			if len(parts) == 3 && hmac.Equal([]byte(parts[2]), []byte(s.joinSignature(parts[1]))) && r.Method == http.MethodPost && r.URL.Path == "/v1/nodes/register" {
				body, err := base64.RawURLEncoding.DecodeString(parts[1])
				var claim joinClaim
				if err == nil && json.Unmarshal(body, &claim) == nil && claim.Expires > s.now().Unix() {
					revision, err := s.Store.Meta("auth:join:" + claim.Node)
					if err == nil && revision == claim.Revision {
						r.Header.Set("X-Tessera-Join", claim.Node)
						next.ServeHTTP(w, r)
						return
					}
				}
			}
		} else if strings.HasPrefix(token, "tessera-gateway.") {
			parts := strings.Split(token, ".")
			if len(parts) == 3 {
				raw, _ := s.Store.Meta("auth:gateway:" + parts[1])
				var credential gatewayCredential
				if json.Unmarshal([]byte(raw), &credential) == nil && subtle.ConstantTimeCompare([]byte(tokenHash(token)), []byte(credential.Hash)) == 1 {
					path := r.URL.EscapedPath()
					if r.Method == http.MethodGet && (path == "/v1/routes/"+url.PathEscape(credential.Route)+"/backends" || path == "/v1/leader" || path == "/v1/controllers") {
						next.ServeHTTP(w, r)
						return
					}
					http.Error(w, "gateway credential does not authorize this operation", 403)
					return
				}
			}
		} else if strings.HasPrefix(token, "tessera-node.") {
			parts := strings.Split(token, ".")
			if len(parts) == 3 {
				expected, _ := s.Store.Meta("auth:node:" + parts[1])
				if expected != "" && subtle.ConstantTimeCompare([]byte(tokenHash(token)), []byte(expected)) == 1 {
					if s.allowNode(r, parts[1]) {
						r.Header.Set("X-Tessera-Node", parts[1])
						next.ServeHTTP(w, r)
						return
					}
					http.Error(w, "node credential does not authorize this operation", 403)
					return
				}
			}
		}
		http.Error(w, "unauthorized", 401)
	})
}

func (s *Server) allowNode(r *http.Request, node string) bool {
	path := r.URL.Path
	if r.Method == http.MethodGet && (path == "/v1/leader" || path == "/v1/controllers" || path == "/v1/nodes/"+node) {
		return true
	}
	if r.Method == http.MethodPost && (path == "/v1/nodes/register" || path == "/v1/nodes/"+node+"/heartbeat") {
		return true
	}
	if r.Method == http.MethodGet && path == "/v1/nodes/"+node+"/assignments" {
		return true
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if r.Method == http.MethodPost && len(parts) == 4 && parts[0] == "v1" {
		if parts[1] == "assignments" && parts[3] == "status" {
			a, err := s.Store.GetAssignment(parts[2])
			return err == nil && a.NodeID == node
		}
		if parts[1] == "actions" && parts[3] == "result" {
			a, err := s.Store.GetAction(parts[2])
			return err == nil && a.Target == node && a.TargetKind != api.KindApp
		}
	}
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && path == "/v1/images" {
		asgs, err := s.Store.ListAssignments()
		if err != nil {
			return false
		}
		for _, a := range asgs {
			if a.NodeID == node && a.Image == r.URL.Query().Get("ref") && api.Active(a.Status) {
				return true
			}
		}
	}
	return false
}

func (s *Server) handleJoinToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Node string `json:"node"`
		TTL  string `json:"ttl"`
	}
	if err := readJSON(w, r, &req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	ttl, err := time.ParseDuration(req.TTL)
	if err != nil || ttl < time.Second || ttl > 24*time.Hour || !scopedNodeID.MatchString(req.Node) {
		http.Error(w, "node and TTL between 1s and 24h required", 400)
		return
	}
	writeJSON(w, 200, map[string]string{"token": s.makeJoinToken(req.Node, ttl)})
}

func (s *Server) handleRevokeNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Node string `json:"node"`
	}
	if err := readJSON(w, r, &req); err != nil || !scopedNodeID.MatchString(req.Node) {
		http.Error(w, "valid node required", 400)
		return
	}
	if err := s.Store.Transaction(func(st *store.Store) error {
		if err := st.DeleteMeta("auth:node:" + req.Node); err != nil {
			return err
		}
		return st.SetMeta("auth:join:"+req.Node, api.NewID())
	}); err != nil {
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
	writeJSON(w, 200, map[string]bool{"revoked": true})
}

func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := readJSON(w, r, &req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if len(req.Token) < 32 || strings.ContainsFunc(req.Token, unicode.IsControl) {
		http.Error(w, "new token must contain at least 32 characters without control characters", 400)
		return
	}
	nodes, err := s.Store.ListNodes()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for _, node := range nodes {
		if !node.Restricted {
			http.Error(w, fmt.Sprintf("rejoin %s with a scoped node credential before rotating the API token", node.ID), 409)
			return
		}
	}
	if err := s.Store.SetMeta("auth:admin", tokenHash(req.Token)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.mu.Lock()
	err = s.writeSnapshotLocked(s.now())
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, map[string]bool{"rotated": true})
}
