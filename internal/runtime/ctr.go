package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type CTR struct {
	Bin string
}

func NewCTR() *CTR {
	bin, err := exec.LookPath("ctr")
	if err != nil {
		bin = "ctr"
	}
	return &CTR{Bin: bin}
}

func (c *CTR) Start(ctx context.Context, spec Spec) (Container, error) {
	if _, err := exec.LookPath(c.Bin); err != nil {
		return Container{}, fmt.Errorf("ctr not found")
	}
	ref := spec.Image
	if !strings.Contains(ref, ".") && !strings.Contains(ref, "/") {
		ref = "docker.io/library/" + ref
	}
	if out, err := exec.CommandContext(ctx, c.Bin, "images", "pull", ref).CombinedOutput(); err != nil {
		return Container{}, &StartError{Reason: "pull: " + string(out)}
	}
	args := []string{"run", "-d", "--net-host"}
	for k, v := range spec.Env {
		args = append(args, "--env", k+"="+v)
	}
	args = append(args, ref, spec.Name)
	args = append(args, spec.Command...)
	if out, err := exec.CommandContext(ctx, c.Bin, args...).CombinedOutput(); err != nil {
		return Container{}, fmt.Errorf("ctr run: %s", out)
	}
	return Container{ID: spec.Name, Name: spec.Name, Running: true}, nil
}

func (c *CTR) Stop(ctx context.Context, id string) error {
	_ = exec.CommandContext(ctx, c.Bin, "tasks", "kill", id).Run()
	_ = exec.CommandContext(ctx, c.Bin, "tasks", "delete", id).Run()
	return exec.CommandContext(ctx, c.Bin, "containers", "delete", id).Run()
}

func (c *CTR) List(ctx context.Context) ([]Container, error) {
	out, err := exec.CommandContext(ctx, c.Bin, "containers", "ls", "-q").CombinedOutput()
	if err != nil {
		return nil, err
	}
	var items []Container
	for _, name := range strings.Fields(string(out)) {
		if !strings.HasPrefix(name, "tessera_") {
			continue
		}
		items = append(items, Container{ID: name, Name: name, Running: true})
	}
	return items, nil
}

func (c *CTR) Logs(ctx context.Context, id string) (string, error) {
	return "", nil
}

func (c *CTR) Prune(ctx context.Context) error { return nil }
