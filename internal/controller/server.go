package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"tessera/internal/api"
	"tessera/internal/config"
	"tessera/internal/pki"
	"tessera/internal/proxy"
	"tessera/internal/store"
	"tessera/internal/survive"
)

type Config struct {
	DataDir string
	Token   string
	ID      string
	Epoch   uint64
	URL     string
}

type Server struct {
	Store    *store.Store
	Token    string
	ID       string
	DataDir  string
	URL      string
	Now      func() time.Time
	Discover func(ctx context.Context) ([]survive.Claim, error)

	mu       sync.Mutex
	epoch    uint64
	leading  bool
	expires  time.Time
	rev      int64
	waiters  []chan struct{}
	lastMove map[string]time.Time
	ca       pki.Material
	proxy    *proxy.Proxy
	http     *http.Server
	cancel   context.CancelFunc
}

func New(st *store.Store, cfg Config) (*Server, error) {
	token := cfg.Token
	if token == "" {
		token, _ = st.Meta("token")
	}
	if token == "" {
		token = newToken()
	}
	if err := st.SetMeta("token", token); err != nil {
		return nil, err
	}
	id := cfg.ID
	if id == "" {
		id, _ = st.Meta("controller_id")
	}
	if id == "" {
		id = "controller-" + api.NewID()
	}
	if err := st.SetMeta("controller_id", id); err != nil {
		return nil, err
	}
	ca, err := loadCA(st)
	if err != nil {
		return nil, err
	}
	epoch := cfg.Epoch
	if epoch == 0 {
		if v, _ := st.Meta("epoch"); v != "" {
			n, _ := strconv.ParseUint(v, 10, 64)
			epoch = n
		}
	}
	if epoch == 0 {
		epoch = 1
	}
	if err := st.SetMeta("epoch", strconv.FormatUint(epoch, 10)); err != nil {
		return nil, err
	}
	if _, err := st.Policy(); err != nil {
		return nil, err
	}
	if v, _ := st.Meta("policy"); v == "" {
		if err := st.SetPolicy(api.Policy{}); err != nil {
			return nil, err
		}
	}
	s := &Server{
		Store:    st,
		Token:    token,
		ID:       id,
		DataDir:  cfg.DataDir,
		URL:      cfg.URL,
		Now:      time.Now,
		epoch:    epoch,
		leading:  true,
		lastMove: map[string]time.Time{},
		ca:       ca,
		proxy:    proxy.New(),
	}
	s.loadMoves()
	return s, nil
}

func (s *Server) Epoch() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epoch
}

func (s *Server) Leading() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leading
}

func (s *Server) Observe(c survive.Claim) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	best := survive.Preferred([]survive.Claim{{Epoch: s.epoch, ID: s.ID}, c})
	if best.ID != s.ID {
		s.leading = false
		s.expires = time.Unix(0, 0)
		return true
	}
	return false
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/leader", s.handleLeader)
	mux.HandleFunc("POST /v1/apply", s.handleApply)
	mux.HandleFunc("GET /v1/apps", s.handleListApps)
	mux.HandleFunc("GET /v1/apps/{name}", s.handleGetApp)
	mux.HandleFunc("GET /v1/apps/{name}/logs", s.handleLogs)
	mux.HandleFunc("GET /v1/nodes", s.handleListNodes)
	mux.HandleFunc("GET /v1/nodes/{id}", s.handleGetNode)
	mux.HandleFunc("POST /v1/nodes/register", s.handleRegister)
	mux.HandleFunc("POST /v1/nodes/{id}/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("GET /v1/nodes/{id}/assignments", s.handleAssignments)
	mux.HandleFunc("GET /v1/assignments", s.handleListAssignments)
	mux.HandleFunc("POST /v1/assignments/{id}/status", s.handleStatus)
	mux.HandleFunc("GET /v1/actions", s.handleActions)
	mux.HandleFunc("POST /v1/actions/{id}/confirm", s.handleConfirm)
	mux.HandleFunc("POST /v1/actions/{id}/result", s.handleActionResult)
	mux.HandleFunc("/v1/images", s.handleImage)
	mux.HandleFunc("GET /v1/routes", s.handleRoutes)
	mux.HandleFunc("GET /v1/diagnose", s.handleDiagnose)
	mux.HandleFunc("POST /v1/ask", s.handleAsk)
	mux.HandleFunc("GET /v1/snapshot", s.handleSnapshot)
	mux.HandleFunc("POST /v1/election", s.handleElection)
	mux.HandleFunc("POST /v1/act", s.handleAct)
	return s.auth(mux)
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.Now == nil {
		s.Now = time.Now
	}
	s.URL = clientURL(ln.Addr())
	if s.DataDir != "" {
		_ = config.Save(s.DataDir, config.File{URL: s.URL, Token: s.Token})
	}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mu.Lock()
	s.expires = s.Now().Add(s.mustPolicy().Lease())
	s.mu.Unlock()
	go s.loop(ctx)
	go s.watchPeers(ctx)
	s.http = &http.Server{Handler: s.Handler()}
	err := s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.proxy != nil {
		s.proxy.Close()
	}
	if s.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return s.http.Shutdown(ctx)
	}
	return nil
}

func (s *Server) loop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	_ = s.Reconcile(s.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Reconcile(s.Now())
		}
	}
}

func (s *Server) watchPeers(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.Discover == nil {
				continue
			}
			claims, err := s.Discover(ctx)
			if err != nil {
				continue
			}
			for _, c := range claims {
				if c.ID != s.ID {
					s.Observe(c)
				}
			}
		}
	}
}

func (s *Server) mustPolicy() api.Policy {
	p, err := s.Store.Policy()
	if err != nil {
		return api.Policy{}
	}
	return p
}

func (s *Server) loadMoves() {
	apps, err := s.Store.ListApps()
	if err != nil {
		return
	}
	for _, a := range apps {
		v, _ := s.Store.Meta("move:" + a.Name)
		if v == "" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		s.lastMove[a.Name] = time.Unix(n, 0)
	}
}

func loadCA(st *store.Store) (pki.Material, error) {
	cert, _ := st.Meta("ca_cert")
	key, _ := st.Meta("ca_key")
	if cert != "" && key != "" {
		return pki.Material{CertPEM: []byte(cert), KeyPEM: []byte(key)}, nil
	}
	m, err := pki.NewCA(time.Now())
	if err != nil {
		return pki.Material{}, err
	}
	if err := st.SetMeta("ca_cert", string(m.CertPEM)); err != nil {
		return pki.Material{}, err
	}
	if err := st.SetMeta("ca_key", string(m.KeyPEM)); err != nil {
		return pki.Material{}, err
	}
	return m, nil
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func clientURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v)
}
