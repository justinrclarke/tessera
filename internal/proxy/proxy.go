package proxy

import (
	"io"
	"net"
	"strconv"
	"sync"
)

type Proxy struct {
	mu    sync.Mutex
	lns   map[int]net.Listener
	backs map[string]string
	names map[int]string
}

func New() *Proxy {
	return &Proxy{lns: map[int]net.Listener{}, backs: map[string]string{}, names: map[int]string{}}
}

func (p *Proxy) Set(name string, port int, backend string) error {
	if port == 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.backs[name] = backend
	if _, ok := p.lns[port]; ok {
		p.names[port] = name
		return nil
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	p.lns[port] = ln
	p.names[port] = name
	go p.serve(ln, port)
	return nil
}

func (p *Proxy) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ln := range p.lns {
		_ = ln.Close()
	}
}

func (p *Proxy) serve(ln net.Listener, port int) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go p.pipe(c, port)
	}
}

func (p *Proxy) pipe(c net.Conn, port int) {
	defer c.Close()
	p.mu.Lock()
	name := p.names[port]
	backend := p.backs[name]
	p.mu.Unlock()
	if backend == "" {
		return
	}
	up, err := net.Dial("tcp", backend)
	if err != nil {
		return
	}
	defer up.Close()
	go io.Copy(up, c)
	_, _ = io.Copy(c, up)
}
