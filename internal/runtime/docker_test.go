package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDockerStartsWithReservedGPUDevice(t *testing.T) {
	d := NewDocker("unused")
	created := false
	d.http = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		switch {
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/images/"):
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/containers/create"):
			var input struct {
				HostConfig struct {
					DeviceRequests []struct {
						Count        int        `json:"Count"`
						DeviceIDs    []string   `json:"DeviceIDs"`
						Capabilities [][]string `json:"Capabilities"`
					} `json:"DeviceRequests"`
				} `json:"HostConfig"`
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			reqs := input.HostConfig.DeviceRequests
			if len(reqs) != 1 || reqs[0].Count != 0 || len(reqs[0].DeviceIDs) != 1 || reqs[0].DeviceIDs[0] != "GPU-123" || len(reqs[0].Capabilities) != 1 || len(reqs[0].Capabilities[0]) != 1 || reqs[0].Capabilities[0][0] != "gpu" {
				t.Fatalf("GPU request: %+v", reqs)
			}
			created = true
			body = `{"Id":"container-1"}`
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/start"):
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/json"):
			body = `{"State":{"Running":true}}`
		default:
			t.Fatalf("unexpected Docker request: %s %s", req.Method, req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	got, err := d.Start(context.Background(), Spec{Name: "tessera_model", Image: "model:latest", GPUs: 1, GPUDevices: []string{"GPU-123"}})
	if err != nil || !created || !got.Running {
		t.Fatalf("Docker GPU start: %+v, %v", got, err)
	}
}
