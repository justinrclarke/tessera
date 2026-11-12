package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/proxy"
)

func TestGatewayPairSurvivesOneGatewayAndControllerLoss(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "healthy") }))
	defer backend.Close()
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.RouteBackends{Epoch: 1, Backends: []string{backend.Listener.Addr().String()}})
	}))
	defer control.Close()
	port := func() int {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		return p
	}
	var gateways []*Gateway
	var addresses []string
	for range 2 {
		g := &Gateway{Client: client.New(control.URL, "token"), Route: "web", Port: port()}
		defer g.Close()
		if err := g.Sync(context.Background()); err != nil {
			t.Fatal(err)
		}
		gateways = append(gateways, g)
		addresses = append(addresses, fmt.Sprintf("127.0.0.1:%d", g.Port))
	}
	front := proxy.New()
	defer front.Close()
	listen := port()
	if err := front.SetBackends("gateways", listen, addresses); err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	reader := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	check := func() {
		t.Helper()
		for range 20 {
			resp, err := reader.Get(fmt.Sprintf("http://127.0.0.1:%d", listen))
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(body) != "healthy" {
				t.Fatalf("%q %v", body, err)
			}
		}
	}
	check()
	control.Close()
	for _, g := range gateways {
		if err := g.Sync(context.Background()); err == nil {
			t.Fatal("controller loss was hidden")
		}
	}
	gateways[0].Close()
	check()
}
