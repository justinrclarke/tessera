package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type Docker struct {
	sock string
	http *http.Client
}

func NewDocker(sock string) *Docker {
	return &Docker{
		sock: sock,
		http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		}},
	}
}

func (d *Docker) Start(ctx context.Context, spec Spec) (Container, error) {
	if err := d.pull(ctx, spec.Image); err != nil {
		return Container{}, &StartError{Reason: "pull: " + err.Error()}
	}
	body := map[string]any{
		"Image": spec.Image,
		"Env":   envList(spec.Env),
		"Cmd":   spec.Command,
	}
	exposed := map[string]struct{}{}
	bindings := map[string][]map[string]string{}
	for _, p := range spec.Ports {
		key := fmt.Sprintf("%d/tcp", p.Container)
		exposed[key] = struct{}{}
		host := ""
		if p.Host != 0 {
			host = fmt.Sprintf("%d", p.Host)
		}
		bindings[key] = []map[string]string{{"HostPort": host}}
	}
	if len(exposed) > 0 {
		body["ExposedPorts"] = exposed
	}
	hostConfig := map[string]any{}
	if len(bindings) > 0 {
		hostConfig["PortBindings"] = bindings
	}
	if spec.Resources.Memory > 0 {
		hostConfig["Memory"] = spec.Resources.Memory
	}
	if spec.Resources.CPU > 0 {
		hostConfig["NanoCPUs"] = spec.Resources.CPU * 1_000_000
	}
	if spec.GPUs > 0 {
		request := map[string]any{"Count": spec.GPUs, "Capabilities": [][]string{{"gpu"}}}
		if len(spec.GPUDevices) > 0 {
			request["Count"] = 0
			request["DeviceIDs"] = spec.GPUDevices
		}
		hostConfig["DeviceRequests"] = []map[string]any{request}
	}
	body["HostConfig"] = hostConfig
	raw, _ := json.Marshal(body)
	resp, err := d.call(ctx, http.MethodPost, "/v1.41/containers/create?name="+url.QueryEscape(spec.Name), raw)
	if err != nil {
		return Container{}, err
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(resp, &created); err != nil || created.ID == "" {
		return Container{}, fmt.Errorf("create %s: %s", spec.Name, trim(resp))
	}
	if _, err := d.call(ctx, http.MethodPost, "/v1.41/containers/"+created.ID+"/start", nil); err != nil {
		return Container{}, err
	}
	c := Container{ID: created.ID, Name: spec.Name, Running: true}
	if inspected, err := d.inspect(ctx, created.ID); err == nil {
		c.HostPort = inspected.HostPort
		c.Running = inspected.Running
	}
	return c, nil
}

func (d *Docker) Stop(ctx context.Context, id string) error {
	_, _ = d.call(ctx, http.MethodPost, "/v1.41/containers/"+id+"/stop?t=2", nil)
	_, err := d.call(ctx, http.MethodDelete, "/v1.41/containers/"+id+"?force=1", nil)
	return err
}

func (d *Docker) List(ctx context.Context) ([]Container, error) {
	resp, err := d.call(ctx, http.MethodGet, `/v1.41/containers/json?all=1`, nil)
	if err != nil {
		return nil, err
	}
	var items []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		State string   `json:"State"`
		Ports []struct {
			PublicPort int `json:"PublicPort"`
		} `json:"Ports"`
	}
	if err := json.Unmarshal(resp, &items); err != nil {
		return nil, err
	}
	var out []Container
	for _, it := range items {
		name := ""
		for _, n := range it.Names {
			n = strings.TrimPrefix(n, "/")
			if strings.HasPrefix(n, "tessera_") {
				name = n
			}
		}
		if name == "" {
			continue
		}
		c := Container{ID: it.ID, Name: name, Running: it.State == "running"}
		if len(it.Ports) > 0 {
			c.HostPort = it.Ports[0].PublicPort
		}
		if !c.Running {
			c.ExitCode = 1
			c.Reason = it.State
		}
		out = append(out, c)
	}
	return out, nil
}

func (d *Docker) Logs(ctx context.Context, id string) (string, error) {
	resp, err := d.call(ctx, http.MethodGet, "/v1.41/containers/"+id+"/logs?stdout=1&stderr=1&tail=200", nil)
	if err != nil {
		return "", err
	}
	return decodeDockerLogs(resp), nil
}

func (d *Docker) Prune(ctx context.Context) error {
	_, err := d.call(ctx, http.MethodPost, "/v1.41/images/prune", nil)
	return err
}

func (d *Docker) HasImage(ctx context.Context, ref string) (bool, error) {
	_, err := d.call(ctx, http.MethodGet, "/v1.41/images/"+url.PathEscape(imageRef(ref))+"/json", nil)
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (d *Docker) ExportImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.41/images/"+url.PathEscape(imageRef(ref))+"/get", nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, fmt.Errorf("docker export: %s", trim(b))
	}
	return resp.Body, nil
}

func (d *Docker) ImportImage(ctx context.Context, ref string, r io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/v1.41/images/load", r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("docker import: %s", trim(b))
	}
	return nil
}

func imageRef(image string) string {
	name, tag := splitRef(image)
	if tag == "" {
		return name
	}
	return name + ":" + tag
}

func (d *Docker) pull(ctx context.Context, image string) error {
	name, tag := splitRef(image)
	ref := name
	if tag != "" {
		ref = name + ":" + tag
	}
	if _, err := d.call(ctx, http.MethodGet, "/v1.41/images/"+url.PathEscape(ref)+"/json", nil); err == nil {
		return nil
	}
	q := url.Values{}
	q.Set("fromImage", name)
	if tag != "" {
		q.Set("tag", tag)
	}
	_, err := d.call(ctx, http.MethodPost, "/v1.41/images/create?"+q.Encode(), nil)
	return err
}

func (d *Docker) inspect(ctx context.Context, id string) (Container, error) {
	resp, err := d.call(ctx, http.MethodGet, "/v1.41/containers/"+id+"/json", nil)
	if err != nil {
		return Container{}, err
	}
	var body struct {
		State struct {
			Running   bool `json:"Running"`
			OOMKilled bool `json:"OOMKilled"`
			ExitCode  int  `json:"ExitCode"`
		} `json:"State"`
		NetworkSettings struct {
			Ports map[string][]struct {
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(resp, &body); err != nil {
		return Container{}, err
	}
	c := Container{ID: id, Running: body.State.Running, ExitCode: body.State.ExitCode, OOM: body.State.OOMKilled}
	if c.OOM {
		c.Reason = "oom"
	}
	for _, binds := range body.NetworkSettings.Ports {
		if len(binds) > 0 && binds[0].HostPort != "" {
			fmt.Sscanf(binds[0].HostPort, "%d", &c.HostPort)
			break
		}
	}
	return c, nil
}

func (d *Docker) call(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("docker %s %s: %s", method, path, trim(b))
	}
	return b, nil
}

func splitRef(image string) (string, string) {
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon > slash {
		return image[:colon], image[colon+1:]
	}
	return image, "latest"
}

func envList(env map[string]string) []string {
	var out []string
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func decodeDockerLogs(b []byte) string {
	var out bytes.Buffer
	for len(b) >= 8 {
		size := int(binary.BigEndian.Uint32(b[4:8]))
		b = b[8:]
		if size < 0 || size > len(b) {
			out.Write(b)
			break
		}
		out.Write(b[:size])
		b = b[size:]
	}
	if out.Len() == 0 {
		return string(b)
	}
	return out.String()
}

func trim(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
