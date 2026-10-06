package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tessera/internal/api"
)

type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

func New(base, token string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

type RegisterRequest struct {
	ID        string            `json:"id"`
	Addr      string            `json:"addr"`
	Capacity  api.Resources     `json:"capacity"`
	Free      api.Resources     `json:"free"`
	Perf      api.Perf          `json:"perf"`
	GPUs      int               `json:"gpus"`
	DiskFree  int64             `json:"disk_free"`
	DiskTotal int64             `json:"disk_total"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type RegisterResponse struct {
	CertPEM   string    `json:"cert_pem"`
	KeyPEM    string    `json:"key_pem"`
	CAPem     string    `json:"ca_pem"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	Epoch     uint64    `json:"epoch"`
}

type HeartbeatRequest struct {
	Addr      string        `json:"addr"`
	Capacity  api.Resources `json:"capacity"`
	Free      api.Resources `json:"free"`
	Perf      api.Perf      `json:"perf"`
	GPUs      int           `json:"gpus"`
	DiskFree  int64         `json:"disk_free"`
	DiskTotal int64         `json:"disk_total"`
}

type HeartbeatResponse struct {
	Epoch         uint64       `json:"epoch"`
	LeaderID      string       `json:"leader_id"`
	Expires       time.Time    `json:"expires"`
	Leading       bool         `json:"leading"`
	Prune         bool         `json:"prune"`
	CertPEM       string       `json:"cert_pem,omitempty"`
	KeyPEM        string       `json:"key_pem,omitempty"`
	NotBefore     time.Time    `json:"not_before,omitempty"`
	NotAfter      time.Time    `json:"not_after,omitempty"`
	SnapshotIndex uint64       `json:"snapshot_index"`
	Command       *NodeCommand `json:"command,omitempty"`
}

type NodeCommand struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type StatusReport struct {
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	Restarts  int    `json:"restarts"`
	RuntimeID string `json:"runtime_id"`
	HostPort  int    `json:"host_port"`
	Logs      string `json:"logs"`
}

type Page struct {
	Rev         int64            `json:"rev"`
	Assignments []api.Assignment `json:"assignments"`
}

type SignedSnapshot struct {
	Snapshot api.Snapshot `json:"snapshot"`
	Body     string       `json:"body"`
	Sig      string       `json:"sig"`
}

func (c *Client) Health(ctx context.Context) error {
	_, err := c.call(ctx, http.MethodGet, "/v1/health", nil, false)
	return err
}

func (c *Client) Apply(ctx context.Context, body []byte) error {
	_, err := c.call(ctx, http.MethodPost, "/v1/apply", body, true)
	return err
}

func (c *Client) ListApps(ctx context.Context) ([]api.App, error) {
	var out []api.App
	err := c.get(ctx, "/v1/apps", &out)
	return out, err
}

func (c *Client) GetApp(ctx context.Context, name string) (api.App, error) {
	var out api.App
	err := c.get(ctx, "/v1/apps/"+url.PathEscape(name), &out)
	return out, err
}

func (c *Client) ListNodes(ctx context.Context) ([]api.Node, error) {
	var out []api.Node
	err := c.get(ctx, "/v1/nodes", &out)
	return out, err
}

func (c *Client) GetNode(ctx context.Context, id string) (api.Node, error) {
	var out api.Node
	err := c.get(ctx, "/v1/nodes/"+url.PathEscape(id), &out)
	return out, err
}

func (c *Client) ListAssignments(ctx context.Context) ([]api.Assignment, error) {
	var out []api.Assignment
	err := c.get(ctx, "/v1/assignments", &out)
	return out, err
}

func (c *Client) ListActions(ctx context.Context) ([]api.Action, error) {
	var out []api.Action
	err := c.get(ctx, "/v1/actions", &out)
	return out, err
}

func (c *Client) ListRoutes(ctx context.Context) ([]api.Route, error) {
	var out []api.Route
	err := c.get(ctx, "/v1/routes", &out)
	return out, err
}

func (c *Client) Leader(ctx context.Context) (api.Lease, error) {
	var out api.Lease
	err := c.get(ctx, "/v1/leader", &out)
	return out, err
}

func (c *Client) Snapshot(ctx context.Context) (SignedSnapshot, error) {
	var out SignedSnapshot
	err := c.get(ctx, "/v1/snapshot", &out)
	return out, err
}

func (c *Client) Diagnose(ctx context.Context) (json.RawMessage, error) {
	b, err := c.call(ctx, http.MethodGet, "/v1/diagnose", nil, true)
	return b, err
}

func (c *Client) Logs(ctx context.Context, name string) (string, error) {
	var out struct {
		Logs string `json:"logs"`
	}
	err := c.get(ctx, "/v1/apps/"+url.PathEscape(name)+"/logs", &out)
	return out.Logs, err
}

func (c *Client) Confirm(ctx context.Context, id string) (string, error) {
	var out struct {
		Result string `json:"result"`
	}
	err := c.post(ctx, "/v1/actions/"+url.PathEscape(id)+"/confirm", map[string]string{}, &out)
	return out.Result, err
}

func (c *Client) FinishCommand(ctx context.Context, id, result string) error {
	return c.post(ctx, "/v1/actions/"+url.PathEscape(id)+"/result", map[string]string{"result": result}, nil)
}

func (c *Client) HasImage(ctx context.Context, ref string) bool {
	resp, err := c.do(ctx, http.MethodHead, "/v1/images?ref="+url.QueryEscape(ref), nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (c *Client) GetImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/images?ref="+url.QueryEscape(ref), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, fmt.Errorf("GET image: %s", strings.TrimSpace(string(b)))
	}
	return resp.Body, nil
}

func (c *Client) PutImage(ctx context.Context, ref string, r io.Reader) error {
	resp, err := c.do(ctx, http.MethodPut, "/v1/images?ref="+url.QueryEscape(ref), r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("PUT image: %s", strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) Act(ctx context.Context, kind, target, reason string) (string, error) {
	var out struct {
		Result string `json:"result"`
	}
	err := c.post(ctx, "/v1/act", map[string]string{"kind": kind, "target": target, "reason": reason}, &out)
	return out.Result, err
}

func (c *Client) Ask(ctx context.Context, question string) (string, error) {
	b, err := json.Marshal(map[string]string{"question": question})
	if err != nil {
		return "", err
	}
	raw, err := c.call(ctx, http.MethodPost, "/v1/ask", b, true)
	if err != nil {
		return "", err
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.Text, nil
}

func (c *Client) Register(ctx context.Context, req RegisterRequest) (RegisterResponse, error) {
	var out RegisterResponse
	err := c.post(ctx, "/v1/nodes/register", req, &out)
	return out, err
}

func (c *Client) Heartbeat(ctx context.Context, id string, req HeartbeatRequest) (HeartbeatResponse, error) {
	var out HeartbeatResponse
	err := c.post(ctx, "/v1/nodes/"+url.PathEscape(id)+"/heartbeat", req, &out)
	return out, err
}

func (c *Client) Report(ctx context.Context, id string, rep StatusReport) error {
	return c.post(ctx, "/v1/assignments/"+url.PathEscape(id)+"/status", rep, nil)
}

func (c *Client) WaitAssignments(ctx context.Context, nodeID string, since int64, wait time.Duration) (Page, error) {
	q := url.Values{}
	q.Set("since", strconv.FormatInt(since, 10))
	q.Set("wait_ms", strconv.FormatInt(wait.Milliseconds(), 10))
	var out Page
	err := c.get(ctx, "/v1/nodes/"+url.PathEscape(nodeID)+"/assignments?"+q.Encode(), &out)
	return out, err
}

func (c *Client) get(ctx context.Context, path string, dest any) error {
	b, err := c.call(ctx, http.MethodGet, path, nil, true)
	if err != nil {
		return err
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(b, dest)
}

func (c *Client) post(ctx context.Context, path string, body any, dest any) error {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	resp, err := c.call(ctx, http.MethodPost, path, raw, true)
	if err != nil {
		return err
	}
	if dest == nil || len(resp) == 0 {
		return nil
	}
	return json.Unmarshal(resp, dest)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	return c.HTTP.Do(req)
}

func (c *Client) call(ctx context.Context, method, path string, body []byte, auth bool) ([]byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth && c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: %s", method, path, strings.TrimSpace(string(b)))
	}
	return b, nil
}
