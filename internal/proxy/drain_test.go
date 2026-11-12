package proxy

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestRetiredBackendFinishesExistingExchange(t *testing.T) {
	old, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	go func() {
		c, err := old.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.WriteString(c, "A")
		buf := make([]byte, 1)
		if _, err := io.ReadFull(c, buf); err == nil {
			io.WriteString(c, "done")
		}
	}()
	next := banner("B")
	defer next.Close()
	p := New()
	defer p.Close()
	port := freePort(t)
	if err := p.SetBackendsWithDrain("web", port, []string{old.Addr().String()}, time.Second); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if err := p.SetBackendsWithDrain("web", port, []string{next.Addr().String()}, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := readPort(t, port); got != "B" {
		t.Fatalf("new connection reached %q", got)
	}
	if _, err := io.WriteString(c, "x"); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(c)
	if err != nil || string(body) != "done" {
		t.Fatalf("in-flight exchange cut off: %q %v", body, err)
	}
}

func TestDrainDeadlineDoesNotResetOnRefresh(t *testing.T) {
	old, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	go func() {
		c, err := old.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.WriteString(c, "A")
		io.Copy(io.Discard, c)
	}()
	p := New()
	defer p.Close()
	port := freePort(t)
	if err := p.Set("web", port, old.Addr().String()); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if err := p.SetBackendsWithDrain("web", port, nil, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = p.SetBackendsWithDrain("web", port, nil, 50*time.Millisecond)
			}
		}
	}()
	_, err = c.Read(buf)
	close(stop)
	<-done
	if err == nil {
		t.Fatal("drained connection stayed open")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("refresh extended the draining connection indefinitely")
	}
}
