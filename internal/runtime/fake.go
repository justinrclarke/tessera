package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
)

type Fake struct {
	mu         sync.Mutex
	items      map[string]Container
	images     map[string][]byte
	Pulls      []string
	FailNext   string
	FailHealth bool
}

func NewFake() *Fake {
	return &Fake{items: map[string]Container{}}
}

func (f *Fake) Start(ctx context.Context, spec Spec) (Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.items[spec.Name]; exists {
		return Container{}, &StartError{Reason: "container already exists"}
	}
	if f.FailNext != "" {
		reason := f.FailNext
		f.FailNext = ""
		return Container{}, &StartError{Reason: reason}
	}
	if spec.Image != "" {
		if f.images == nil {
			f.images = map[string][]byte{}
		}
		if _, ok := f.images[spec.Image]; !ok {
			f.Pulls = append(f.Pulls, spec.Image)
			f.images[spec.Image] = []byte(spec.Image)
		}
	}
	c := Container{ID: spec.Name, Name: spec.Name, Running: true, HostPort: 18080}
	if len(spec.Ports) > 0 && spec.Ports[0].Host != 0 {
		c.HostPort = spec.Ports[0].Host
	}
	f.items[spec.Name] = c
	return c, nil
}

func (f *Fake) Stop(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, id)
	return nil
}

func (f *Fake) List(ctx context.Context) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Container
	for _, c := range f.items {
		out = append(out, c)
	}
	return out, nil
}

func (f *Fake) Logs(ctx context.Context, id string) (string, error) {
	return "fake log " + id, nil
}

func (f *Fake) Prune(ctx context.Context) error { return nil }

func (f *Fake) HasImage(ctx context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.images == nil {
		return false, nil
	}
	_, ok := f.images[ref]
	return ok, nil
}

func (f *Fake) ExportImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.images[ref]
	if !ok {
		return nil, fmt.Errorf("missing image %s", ref)
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), b...))), nil
}

func (f *Fake) ImportImage(ctx context.Context, ref string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.images == nil {
		f.images = map[string][]byte{}
	}
	f.images[ref] = b
	return nil
}

func (f *Fake) Kill(id, reason string, oom bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.items[id]
	c.Running = false
	c.ExitCode = 1
	c.Reason = reason
	c.OOM = oom
	f.items[id] = c
}

type StartError struct{ Reason string }

func (e *StartError) Error() string { return e.Reason }
