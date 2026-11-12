package runtime

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDockerPullPreservesRegistryDigest(t *testing.T) {
	ref := "registry.example:5000/team/web@sha256:" + strings.Repeat("a", 64)
	d := NewDocker("unused")
	pulled := false
	d.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		status, body := 200, `{}`
		if r.Method == http.MethodGet {
			if !strings.Contains(r.URL.Path, ref) {
				t.Fatalf("inspect lost digest: %s", r.URL)
			}
			status = 404
		} else {
			if r.URL.Query().Get("fromImage") != ref || r.URL.Query().Get("tag") != "" {
				t.Fatalf("digest was interpreted as a mutable tag: %s", r.URL)
			}
			pulled = true
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	if err := d.pull(context.Background(), ref); err != nil || !pulled {
		t.Fatalf("pull failed: %v", err)
	}
}

func TestDockerPullRejectsErrorInsideSuccessfulHTTPStream(t *testing.T) {
	d := NewDocker("unused")
	d.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		status, body := 200, "{\"status\":\"Pulling\"}\n{\"errorDetail\":{\"message\":\"registry denied\"},\"error\":\"registry denied\"}\n"
		if r.Method == http.MethodGet {
			status, body = 404, `{}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	if err := d.pull(context.Background(), "example/web:latest"); err == nil || !strings.Contains(err.Error(), "registry denied") {
		t.Fatalf("failed registry pull appeared successful: %v", err)
	}
}
