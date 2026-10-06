package agent

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"tessera/internal/api"
	"tessera/internal/runtime"
)

type probeState struct {
	RuntimeID string    `json:"runtime_id"`
	Started   time.Time `json:"started"`
	Ready     bool      `json:"ready"`
}

func (a *Agent) reportReady(ctx context.Context, d api.Assignment, c runtime.Container) {
	if d.Health == nil {
		a.report(ctx, d, api.StatusRunning, "", c)
		return
	}
	state := a.probes[d.ID]
	if state.RuntimeID != c.ID || state.Started.IsZero() {
		state = probeState{RuntimeID: c.ID, Started: a.now()}
	}
	probeCtx, cancel := context.WithTimeout(ctx, healthDuration(d.Health.Timeout, 2*time.Second))
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", c.HostPort, d.Health.Path), nil)
	ready := false
	if err == nil && c.HostPort > 0 {
		cl := &http.Client{Transport: readinessTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := cl.Do(req)
		if err == nil {
			ready = resp.StatusCode >= 200 && resp.StatusCode < 300
			resp.Body.Close()
		}
	}
	if ready {
		state.Ready = true
		a.probes[d.ID] = state
		a.report(ctx, d, api.StatusRunning, "", c)
		return
	}
	a.probes[d.ID] = state
	if !state.Ready && a.now().Sub(state.Started) >= healthDuration(d.Health.StartupTimeout, 60*time.Second) {
		if err := a.Runtime.Stop(ctx, c.ID); err != nil {
			a.report(ctx, d, api.StatusStarting, "health: startup timeout; stop failed: "+err.Error(), c)
			return
		}
		a.restarts[d.ID] = a.max()
		a.report(ctx, d, api.StatusFailed, "health: startup timeout", c)
		return
	}
	a.report(ctx, d, api.StatusStarting, "health: waiting for HTTP readiness", c)
}

var readinessTransport = &http.Transport{}

func healthDuration(value string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
