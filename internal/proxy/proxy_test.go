package proxy

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestCutover(t *testing.T) {
	a := banner("A")
	defer a.Close()
	b := banner("B")
	defer b.Close()
	p := New()
	defer p.Close()
	port := freePort(t)
	if err := p.Set("web", port, a.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if got := readPort(t, port); got != "A" {
		t.Fatalf("before %q", got)
	}
	hold := sticky(t)
	defer hold.Close()
	if err := p.Set("web", port, hold.Addr().String()); err != nil {
		t.Fatal(err)
	}
	held, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	time.Sleep(50 * time.Millisecond)
	if err := p.Set("web", port, b.Addr().String()); err != nil {
		t.Fatal(err)
	}
	_ = held.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 8)
	if _, err := held.Read(buf); err == nil {
		t.Fatal("old connection stayed open")
	}
	if got := readPort(t, port); got != "B" {
		t.Fatalf("after %q", got)
	}
}

func banner(msg string) net.Listener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.WriteString(c, msg)
			}(c)
		}
	}()
	return ln
}

func sticky(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(io.Discard, c)
			}(c)
		}
	}()
	return ln
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, ps, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(ps)
	return n
}

func readPort(t *testing.T, port int) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	b, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBackendsBalanceAndSurviveAnUnreachableReplica(t *testing.T) {
	a := banner("A")
	defer a.Close()
	b := banner("B")
	defer b.Close()
	p := New()
	defer p.Close()
	port := freePort(t)
	if err := p.SetBackends("web", port, []string{a.Addr().String(), b.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		seen[readPort(t, port)]++
	}
	if seen["A"] != 3 || seen["B"] != 3 {
		t.Fatalf("unbalanced backends: %+v", seen)
	}
	a.Close()
	for i := 0; i < 4; i++ {
		if got := readPort(t, port); got != "B" {
			t.Fatalf("failed replica interrupted route: %q", got)
		}
	}
	p.RemoveExcept(map[string]bool{})
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second); err == nil {
		c.Close()
		t.Fatal("removed route still accepts connections")
	}
}

func TestBackendRefreshKeepsExistingConnections(t *testing.T) {
	ln := sticky(t)
	defer ln.Close()
	p := New()
	defer p.Close()
	port := freePort(t)
	if err := p.SetBackends("web", port, []string{ln.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(30 * time.Millisecond)
	if err := p.SetBackends("web", port, []string{ln.Addr().String(), "127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("unexpected response")
	} else if e, ok := err.(net.Error); !ok || !e.Timeout() {
		t.Fatalf("refresh closed a retained backend connection: %v", err)
	}
}
