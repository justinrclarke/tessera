package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"tessera/internal/client"
	"tessera/internal/proxy"
)

type Gateway struct {
	Client  *client.Client
	Route   string
	Port    int
	OnError func(error)
	proxy   *proxy.Proxy
	epoch   uint64
}

func (g *Gateway) Sync(ctx context.Context) error {
	if g.Client == nil || g.Route == "" || g.Port < 1 || g.Port > 65535 {
		return fmt.Errorf("gateway requires a controller client, route, and listen port")
	}
	if g.proxy == nil {
		g.proxy = proxy.New()
	}
	state, err := g.Client.RouteBackends(ctx, g.Route)
	if err != nil {
		var response *client.HTTPError
		if errors.As(err, &response) && (response.Status == http.StatusNotFound || response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden) {
			g.proxy.RemoveExcept(map[string]bool{})
		}
		return err
	}
	if state.Epoch < g.epoch {
		return fmt.Errorf("gateway rejected stale controller epoch %d", state.Epoch)
	}
	if err := g.proxy.SetBackends(g.Route, g.Port, state.Backends); err != nil {
		return err
	}
	g.epoch = state.Epoch
	return nil
}

func (g *Gateway) Run(ctx context.Context) error {
	defer g.Close()
	refresh := func() error {
		pollCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		return g.Sync(pollCtx)
	}
	if err := refresh(); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			err := refresh()
			if err == nil {
				last = ""
				continue
			}
			if g.OnError != nil && err.Error() != last {
				g.OnError(err)
			}
			last = err.Error()
		}
	}
}

func (g *Gateway) Close() {
	if g.proxy != nil {
		g.proxy.Close()
	}
}
