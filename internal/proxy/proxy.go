package proxy

import (
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"tessera/internal/api"
)

type connection struct {
	client  net.Conn
	up      net.Conn
	backend string
}

type Proxy struct {
	mu    sync.Mutex
	lns   map[int]net.Listener
	backs map[string][]string
	names map[int]string
	conns map[int]map[*connection]struct{}
	next  map[int]int
}

func New() *Proxy {
	return &Proxy{lns: map[int]net.Listener{}, backs: map[string][]string{}, names: map[int]string{}, conns: map[int]map[*connection]struct{}{}, next: map[int]int{}}
}

func (p *Proxy) Set(name string, port int, backend string) error {
	var backends []string
	if backend != "" {
		backends = []string{backend}
	}
	return p.SetBackends(name, port, backends)
}

func (p *Proxy) SetBackends(name string, port int, backends []string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("route %s requires a port between 1 and 65535", name)
	}
	backends = append([]string(nil), backends...)
	sort.Strings(backends)
	p.mu.Lock()
	defer p.mu.Unlock()
	if owner := p.names[port]; owner != "" && owner != name {
		return fmt.Errorf("route port %d is already used by %s", port, owner)
	}
	if _, ok := p.lns[port]; !ok {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			return err
		}
		p.lns[port] = ln
		p.names[port] = name
		go p.serve(ln, port)
	}
	for old, owner := range p.names {
		if owner == name && old != port {
			p.removeLocked(old)
		}
	}
	p.backs[name] = backends
	for c := range p.conns[port] {
		if !contains(backends, c.backend) {
			c.client.Close()
			if c.up != nil {
				c.up.Close()
			}
		}
	}
	return nil
}

func (p *Proxy) RemoveExcept(names map[string]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for port, name := range p.names {
		if !names[name] {
			p.removeLocked(port)
		}
	}
}

func (p *Proxy) removeLocked(port int) {
	if ln := p.lns[port]; ln != nil {
		ln.Close()
	}
	for c := range p.conns[port] {
		c.client.Close()
		if c.up != nil {
			c.up.Close()
		}
	}
	delete(p.backs, p.names[port])
	delete(p.lns, port)
	delete(p.names, port)
	delete(p.next, port)
}

func (p *Proxy) Close() {
	p.RemoveExcept(map[string]bool{})
}

func (p *Proxy) serve(ln net.Listener, port int) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go p.pipe(c, port, ln)
	}
}

func (p *Proxy) pipe(client net.Conn, port int, ln net.Listener) {
	defer client.Close()
	p.mu.Lock()
	if p.lns[port] != ln {
		p.mu.Unlock()
		return
	}
	backends := append([]string(nil), p.backs[p.names[port]]...)
	if len(backends) == 0 {
		p.mu.Unlock()
		return
	}
	start := p.next[port] % len(backends)
	p.next[port] = (start + 1) % len(backends)
	p.mu.Unlock()
	var up net.Conn
	var backend string
	for i := range backends {
		backend = backends[(start+i)%len(backends)]
		var err error
		up, err = net.DialTimeout("tcp", backend, time.Second)
		if err == nil {
			break
		}
	}
	if up == nil {
		return
	}
	defer up.Close()
	c := &connection{client: client, up: up, backend: backend}
	p.mu.Lock()
	if p.lns[port] != ln || !contains(p.backs[p.names[port]], backend) {
		p.mu.Unlock()
		return
	}
	if p.conns[port] == nil {
		p.conns[port] = map[*connection]struct{}{}
	}
	p.conns[port][c] = struct{}{}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.conns[port], c); p.mu.Unlock() }()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(up, client)
		if tcp, ok := up.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.Copy(client, up)
	client.Close()
	up.Close()
	<-done
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func Backends(r api.Route, asgs []api.Assignment, nodes []api.Node) []string {
	byNode := map[string]api.Node{}
	for _, n := range nodes {
		byNode[n.ID] = n
	}
	seen := map[string]bool{}
	var result []string
	for _, asg := range asgs {
		if asg.App != r.App || asg.Status != api.StatusRunning {
			continue
		}
		n, ok := byNode[asg.NodeID]
		if !ok || n.Status == api.NodeDead {
			continue
		}
		port := asg.HostPort
		if port == 0 {
			port = r.TargetPort
		}
		if port == 0 {
			continue
		}
		host := n.Addr
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host == "" {
			host = "127.0.0.1"
		}
		backend := net.JoinHostPort(host, strconv.Itoa(port))
		if !seen[backend] {
			result = append(result, backend)
			seen[backend] = true
		}
	}
	sort.Strings(result)
	return result
}
