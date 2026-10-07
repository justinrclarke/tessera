package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tessera/internal/api"
	"tessera/internal/pki"
	"tessera/internal/store"

	"github.com/hashicorp/raft"
)

type ControllerPeer struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	URL     string `json:"url"`
}

type ReplicaConfig struct {
	Listen           string
	Peers            []ControllerPeer
	Bootstrap        bool
	Transport        raft.Transport
	HeartbeatTimeout time.Duration
	RequestTimeout   time.Duration
}

type replication struct {
	raft      *raft.Raft
	transport raft.Transport
	peers     []ControllerPeer
	timeout   time.Duration
	mu        sync.Mutex
	term      atomic.Uint64
	failed    atomic.Bool
}

var errRejectedRequest = errors.New("request rejected")

func validateReplicas(cfg Config) error {
	if cfg.DataDir == "" || cfg.Token == "" || cfg.ID == "" || len(cfg.Replicas.Peers) != 3 {
		return fmt.Errorf("replication requires a data directory, shared token, controller ID, and exactly three peers")
	}
	if cfg.Epoch != 0 {
		return fmt.Errorf("replica epochs are committed through consensus")
	}
	if cfg.Replicas.Transport == nil {
		if _, err := replicaPort(cfg.Replicas.Listen); err != nil {
			return fmt.Errorf("replication requires a fixed Raft listen port: %w", err)
		}
	}
	ids, addresses, urls := map[string]bool{}, map[string]bool{}, map[string]bool{}
	self := false
	for _, peer := range cfg.Replicas.Peers {
		u, err := url.Parse(peer.URL)
		if peer.ID == "" || peer.Address == "" || ids[peer.ID] || addresses[peer.Address] || urls[peer.URL] || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("each replica requires a distinct ID, Raft address, and HTTP controller URL")
		}
		if cfg.Replicas.Transport == nil {
			host, err := replicaPort(peer.Address)
			if err != nil {
				return fmt.Errorf("invalid Raft address for %s: %w", peer.ID, err)
			}
			if host == "" {
				return fmt.Errorf("Raft address for %s requires a reachable host", peer.ID)
			}
		}
		ids[peer.ID], addresses[peer.Address], urls[peer.URL] = true, true, true
		if peer.ID == cfg.ID {
			self = true
			if cfg.URL != peer.URL {
				return fmt.Errorf("controller URL must match its peer URL")
			}
		}
	}
	if !self {
		return fmt.Errorf("controller ID is absent from the replica list")
	}
	return nil
}

func replicaPort(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("port must be between 1 and 65535")
	}
	return host, nil
}

func (s *Server) startReplication(cfg ReplicaConfig) error {
	peers := append([]ControllerPeer(nil), cfg.Peers...)
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
	body, err := json.Marshal(peers)
	if err != nil {
		return err
	}
	previous, err := s.Store.Meta("controller_raft")
	if err != nil {
		return err
	}
	if previous != "" && previous != string(body) {
		return fmt.Errorf("replica membership differs from the stored configuration")
	}
	storage, err := s.Store.RaftStore()
	if err != nil {
		return err
	}
	snapshotDir := filepath.Join(s.DataDir, "raft-snapshots")
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(snapshotDir, 0o700); err != nil {
		return err
	}
	snapshots, err := raft.NewFileSnapshotStore(snapshotDir, 2, io.Discard)
	if err != nil {
		return err
	}
	existing, err := raft.HasExistingState(storage, storage, snapshots)
	if err != nil {
		return err
	}
	transport := cfg.Transport
	if transport == nil {
		stream, err := newRaftStream(cfg.Listen, s.Token)
		if err != nil {
			return err
		}
		for _, peer := range peers {
			if peer.ID == s.ID {
				stream.advertise = replicaAddress(peer.Address)
			}
		}
		transport = raft.NewNetworkTransport(stream, 3, time.Second, io.Discard)
	}
	closeTransport := func() {
		if closer, ok := transport.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	for _, peer := range peers {
		if peer.ID == s.ID && string(transport.LocalAddr()) != peer.Address {
			closeTransport()
			return fmt.Errorf("Raft listen address %s does not match peer address %s", transport.LocalAddr(), peer.Address)
		}
		s.controllers = append(s.controllers, peer.URL)
	}
	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(s.ID)
	rc.LogOutput = io.Discard
	if cfg.HeartbeatTimeout > 0 {
		rc.HeartbeatTimeout = cfg.HeartbeatTimeout
		rc.ElectionTimeout = 2 * cfg.HeartbeatTimeout
		rc.LeaderLeaseTimeout = cfg.HeartbeatTimeout / 2
		rc.CommitTimeout = 10 * time.Millisecond
	}
	s.replica = &replication{transport: transport, peers: peers, timeout: cfg.RequestTimeout}
	if s.replica.timeout <= 0 {
		s.replica.timeout = 3 * time.Second
	}
	s.leading = false
	r, err := raft.NewRaft(rc, &controllerFSM{server: s}, storage, storage, snapshots, transport)
	if err != nil {
		closeTransport()
		return err
	}
	s.replica.raft = r
	if cfg.Bootstrap && !existing {
		configuration := raft.Configuration{}
		for _, peer := range peers {
			configuration.Servers = append(configuration.Servers, raft.Server{ID: raft.ServerID(peer.ID), Address: raft.ServerAddress(peer.Address), Suffrage: raft.Voter})
		}
		if err := r.BootstrapCluster(configuration).Error(); err != nil {
			_ = r.Shutdown().Error()
			closeTransport()
			return err
		}
	}
	if err := s.Store.SetMeta("controller_raft", string(body)); err != nil {
		_ = r.Shutdown().Error()
		closeTransport()
		return err
	}
	return nil
}

func (s *Server) controllerURLs() []string { return append([]string(nil), s.controllers...) }

func (r *replication) ready() bool {
	return r.raft != nil && !r.failed.Load() && r.raft.State() == raft.Leader && r.term.Load() == r.raft.CurrentTerm()
}

func (r *replication) leaderURL() string {
	_, id := r.raft.LeaderWithID()
	for _, peer := range r.peers {
		if peer.ID == string(id) {
			return peer.URL
		}
	}
	return ""
}

func (s *Server) propose(ctx context.Context, now time.Time, fn func(*Server) error) error {
	r := s.replica
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if err := s.ensureEpoch(ctx, now); err != nil {
		return err
	}
	if fn == nil {
		return nil
	}
	batch, err := s.capture(now, fn)
	if err != nil {
		return err
	}
	return s.commit(ctx, batch)
}

func (s *Server) ensureEpoch(ctx context.Context, now time.Time) error {
	r := s.replica
	if r.failed.Load() || r.raft.State() != raft.Leader {
		return raft.ErrNotLeader
	}
	if err := awaitFuture(ctx, r.raft.VerifyLeader()); err != nil {
		return err
	}
	term := r.raft.CurrentTerm()
	if r.term.Load() == term {
		return nil
	}
	if err := awaitFuture(ctx, r.raft.Barrier(r.timeout)); err != nil {
		return err
	}
	batch, err := s.capture(now, func(worker *Server) error {
		oldTerm, err := worker.Store.Meta("election_term")
		if err != nil {
			return err
		}
		oldLeader, err := worker.Store.Meta("elected_controller")
		if err != nil {
			return err
		}
		if oldTerm == strconv.FormatUint(term, 10) && oldLeader == s.ID {
			return nil
		}
		if worker.epoch == ^uint64(0) {
			return fmt.Errorf("leader epoch exhausted")
		}
		worker.epoch++
		for key, value := range map[string]string{
			"epoch": strconv.FormatUint(worker.epoch, 10), "election_term": strconv.FormatUint(term, 10), "elected_controller": s.ID,
			"ca_cert": string(s.ca.CertPEM), "ca_key": string(s.ca.KeyPEM),
		} {
			if err := worker.Store.SetMeta(key, value); err != nil {
				return err
			}
		}
		assignments, err := worker.Store.ListAssignments()
		if err != nil {
			return err
		}
		for _, assignment := range assignments {
			if api.Active(assignment.Status) {
				assignment.Epoch = worker.epoch
				if err := worker.Store.PutAssignment(assignment); err != nil {
					return err
				}
			}
		}
		return worker.writeSnapshotLocked(now)
	})
	if err != nil {
		return err
	}
	if err := s.commit(ctx, batch); err != nil {
		return err
	}
	if r.raft.State() != raft.Leader || r.raft.CurrentTerm() != term {
		return raft.ErrLeadershipLost
	}
	r.term.Store(term)
	return nil
}

func (s *Server) capture(now time.Time, fn func(*Server) error) (store.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Store.Capture(func(staged *store.Store) error {
		worker := &Server{Store: staged, Token: s.Token, ID: s.ID, DataDir: s.DataDir, URL: s.URL, Now: func() time.Time { return now }, epoch: s.epoch, leading: true, rev: s.rev, ca: s.ca, lastMove: map[string]time.Time{}, controllers: s.controllerURLs()}
		worker.loadMoves()
		worker.expires = now.Add(worker.mustPolicy().Lease())
		if err := fn(worker); err != nil {
			return err
		}
		return staged.SetMeta("lease_expires", strconv.FormatInt(worker.expires.UnixNano(), 10))
	})
}

func (s *Server) commit(ctx context.Context, batch store.Batch) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	future := s.replica.raft.Apply(body, s.replica.timeout)
	if err := awaitFuture(ctx, future); err != nil {
		return err
	}
	if err, ok := future.Response().(error); ok {
		return err
	}
	return nil
}

func awaitFuture(ctx context.Context, future raft.Future) error {
	done := make(chan error, 1)
	go func() { done <- future.Error() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) replicatedHandler(next http.Handler) http.Handler {
	if s.replica == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urls, _ := json.Marshal(s.controllerURLs())
		w.Header().Set("X-Tessera-Controllers", string(urls))
		if r.URL.Path == "/v1/health" || r.URL.Path == "/v1/leader" || r.URL.Path == "/v1/controllers" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/v1/election" {
			http.Error(w, "replicated controllers use Raft elections", http.StatusConflict)
			return
		}
		if s.replica.raft.State() != raft.Leader {
			s.rejectReplica(w, r)
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.URL.Path == "/v1/ask" || r.URL.Path == "/v1/images" {
			if err := s.propose(r.Context(), s.now(), nil); err != nil {
				s.rejectReplica(w, r)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		requestID := r.Header.Get("X-Tessera-Request-ID")
		if len(requestID) > 128 {
			http.Error(w, "request ID too long", http.StatusBadRequest)
			return
		}
		response := &replicaResponse{Headers: make(http.Header)}
		err := s.propose(r.Context(), s.now(), func(worker *Server) error {
			if err := worker.Store.PruneReceipts(worker.now()); err != nil {
				return err
			}
			if requestID != "" {
				retention := 24 * time.Hour
				if r.URL.Path == "/v1/nodes/register" || strings.HasSuffix(r.URL.Path, "/heartbeat") || strings.HasSuffix(r.URL.Path, "/status") {
					retention = 2 * time.Minute
				}
				response.Expires = worker.now().Add(retention).Unix()
				cached, err := worker.Store.Meta("request:" + requestID)
				if err != nil {
					return err
				}
				if cached != "" {
					return json.Unmarshal([]byte(cached), response)
				}
			}
			worker.Handler().ServeHTTP(response, r)
			if response.Code >= 300 {
				return errRejectedRequest
			}
			if requestID != "" {
				body, err := json.Marshal(response)
				if err != nil {
					return err
				}
				return worker.Store.SetMeta("request:"+requestID, string(body))
			}
			return nil
		})
		if err != nil && !errors.Is(err, errRejectedRequest) {
			s.rejectReplica(w, r)
			return
		}
		response.writeTo(w)
	})
}

func (s *Server) rejectReplica(w http.ResponseWriter, r *http.Request) {
	leader := s.replica.leaderURL()
	if leader != "" && leader != s.URL {
		w.Header().Set("Location", leader+r.URL.RequestURI())
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	http.Error(w, "controller has no writable majority", http.StatusServiceUnavailable)
}

type replicaResponse struct {
	Headers http.Header `json:"headers"`
	Code    int         `json:"code"`
	Body    []byte      `json:"body"`
	Expires int64       `json:"expires,omitempty"`
}

func (r *replicaResponse) Header() http.Header  { return r.Headers }
func (r *replicaResponse) WriteHeader(code int) { r.Code = code }
func (r *replicaResponse) Write(body []byte) (int, error) {
	if r.Code == 0 {
		r.Code = http.StatusOK
	}
	r.Body = append(r.Body, body...)
	return len(body), nil
}
func (r *replicaResponse) writeTo(w http.ResponseWriter) {
	for key, values := range r.Headers {
		w.Header()[key] = values
	}
	code := r.Code
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_, _ = w.Write(r.Body)
}

type controllerFSM struct{ server *Server }

func (f *controllerFSM) Apply(log *raft.Log) any {
	s := f.server
	s.mu.Lock()
	defer s.mu.Unlock()
	var batch store.Batch
	if err := json.Unmarshal(log.Data, &batch); err != nil {
		s.replica.failed.Store(true)
		return err
	}
	changed, err := s.Store.ApplyBatch(log.Index, batch)
	if err != nil {
		if !errors.Is(err, store.ErrStaleBatch) {
			s.replica.failed.Store(true)
		}
		return err
	}
	if changed {
		if err := s.reloadReplicatedState(); err != nil {
			s.replica.failed.Store(true)
			return err
		}
		s.rev = int64(log.Index)
		s.notifyLocked()
		s.syncProxyLocked()
	}
	return nil
}

func (s *Server) reloadReplicatedState() error {
	value, err := s.Store.Meta("epoch")
	if err != nil {
		return err
	}
	s.epoch, err = strconv.ParseUint(value, 10, 64)
	if err != nil {
		return err
	}
	cert, err := s.Store.Meta("ca_cert")
	if err != nil {
		return err
	}
	key, err := s.Store.Meta("ca_key")
	if err != nil {
		return err
	}
	s.ca = pki.Material{CertPEM: []byte(cert), KeyPEM: []byte(key)}
	s.lastMove = map[string]time.Time{}
	s.loadMoves()
	value, err = s.Store.Meta("lease_expires")
	if err != nil {
		return err
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return err
	}
	s.expires = time.Unix(0, n)
	return nil
}

func (f *controllerFSM) Snapshot() (raft.FSMSnapshot, error) {
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	statements, err := f.server.Store.ExportState()
	if err != nil {
		return nil, err
	}
	return &controllerSnapshot{statements: statements}, nil
}

func (f *controllerFSM) Restore(reader io.ReadCloser) error {
	defer reader.Close()
	var statements []store.Statement
	if err := json.NewDecoder(reader).Decode(&statements); err != nil {
		return err
	}
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	if err := f.server.Store.RestoreState(statements); err != nil {
		return err
	}
	if err := f.server.reloadReplicatedState(); err != nil {
		return err
	}
	value, err := f.server.Store.Meta("fsm_index")
	if err != nil {
		return err
	}
	f.server.rev, err = strconv.ParseInt(value, 10, 64)
	f.server.notifyLocked()
	f.server.syncProxyLocked()
	return err
}

type controllerSnapshot struct{ statements []store.Statement }

func (s *controllerSnapshot) Persist(sink raft.SnapshotSink) error {
	if err := json.NewEncoder(sink).Encode(s.statements); err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *controllerSnapshot) Release() {}

type raftStream struct {
	net.Listener
	config    *tls.Config
	advertise replicaAddress
}

type replicaAddress string

func (a replicaAddress) Network() string { return "tcp" }
func (a replicaAddress) String() string  { return string(a) }

func (s *raftStream) Addr() net.Addr {
	if s.advertise != "" {
		return s.advertise
	}
	return s.Listener.Addr()
}

func newRaftStream(address, token string) (*raftStream, error) {
	seed := sha256.Sum256([]byte("tessera raft TLS credential\x00" + token))
	key := ed25519.NewKeyFromSeed(seed[:])
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tessera-raft"}, DNSNames: []string{"tessera-raft"},
		NotBefore: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, RootCAs: pool, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, ServerName: "tessera-raft"}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	return &raftStream{Listener: tls.NewListener(listener, cfg), config: cfg}, nil
}

func (s *raftStream) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	return tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", string(address), s.config)
}

func (s *raftStream) Accept() (net.Conn, error) {
	for {
		conn, err := s.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = conn.(*tls.Conn).HandshakeContext(ctx)
		cancel()
		if err == nil {
			return conn, nil
		}
		_ = conn.Close()
	}
}
