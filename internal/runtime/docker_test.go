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

func TestDockerListInspectsExitStatusAndOOM(t *testing.T) {
	d := NewDocker("unused")
	d.http = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/v1.41/containers/json":
			body = `[{"Id":"done","Names":["/tessera_done"],"State":"exited"},{"Id":"oom","Names":["/tessera_oom"],"State":"exited"},{"Id":"foreign","Names":["/other"],"State":"exited"}]`
		case "/v1.41/containers/done/json":
			body = `{"State":{"Running":false,"ExitCode":0}}`
		case "/v1.41/containers/oom/json":
			body = `{"State":{"Running":false,"ExitCode":137,"OOMKilled":true}}`
		default:
			t.Fatalf("unexpected request: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	items, err := d.List(context.Background())
	if err != nil || len(items) != 2 || items[0].ExitCode != 0 || items[0].Running || !items[1].OOM || items[1].ExitCode != 137 {
		t.Fatalf("incorrect job completion or OOM state: %+v %v", items, err)
	}
}
