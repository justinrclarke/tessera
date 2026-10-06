package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type CTR struct {
	Bin     string
	Address string
	LogDir  string
	run     func(ctx context.Context, name string, args ...string) ([]byte, error)
	look    func(string) error
}

func NewCTR(addr string) *CTR {
	bin, err := exec.LookPath("ctr")
	if err != nil || bin == "" {
		bin = "ctr"
	}
	if addr == "" {
		addr = "/var/run/containerd/containerd.sock"
	}
	c := &CTR{
		Bin:     bin,
		Address: addr,
		LogDir:  filepath.Join(os.TempDir(), "tessera-logs"),
	}
	c.run = c.exec
	c.look = lookBin
	return c
}

func lookBin(file string) error {
	_, err := exec.LookPath(file)
	return err
}

func (c *CTR) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

func (c *CTR) args(sub ...string) []string {
	base := []string{"--address", c.Address, "-n", "tessera"}
	return append(base, sub...)
}

func (c *CTR) Start(ctx context.Context, spec Spec) (Container, error) {
	if c.look == nil {
		c.look = lookBin
	}
	if err := c.look(c.Bin); err != nil {
		return Container{}, fmt.Errorf("ctr not found")
	}
	ref := ctrRef(spec.Image)
	has, err := c.HasImage(ctx, spec.Image)
	if err != nil || !has {
		if out, pullErr := c.run(ctx, c.Bin, c.args("images", "pull", ref)...); pullErr != nil {
			return Container{}, &StartError{Reason: "pull: " + string(out)}
		}
	}
	if err := os.MkdirAll(c.LogDir, 0o755); err != nil {
		return Container{}, err
	}
	logPath := filepath.Join(c.LogDir, spec.Name+".log")
	args := c.args("run", "-d", "--net-host", "--log-uri", "file://"+logPath)
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+spec.Env[k])
	}
	if spec.Resources.Memory > 0 {
		args = append(args, "--memory-limit", strconv.FormatInt(spec.Resources.Memory, 10))
	}
	if spec.Resources.CPU > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(float64(spec.Resources.CPU)/1000, 'f', -1, 64))
	}
	args = append(args, ref, spec.Name)
	args = append(args, spec.Command...)
	if out, err := c.run(ctx, c.Bin, args...); err != nil {
		return Container{}, &StartError{Reason: "ctr run: " + string(out)}
	}
	return Container{ID: spec.Name, Name: spec.Name, Running: true, HostPort: hostPort(spec)}, nil
}

func (c *CTR) Stop(ctx context.Context, id string) error {
	if !strings.HasPrefix(id, "tessera_") {
		return nil
	}
	_, _ = c.run(ctx, c.Bin, c.args("tasks", "kill", id)...)
	_, _ = c.run(ctx, c.Bin, c.args("tasks", "delete", id)...)
	_, err := c.run(ctx, c.Bin, c.args("containers", "delete", id)...)
	return err
}

func (c *CTR) List(ctx context.Context) ([]Container, error) {
	out, err := c.run(ctx, c.Bin, c.args("containers", "ls", "-q")...)
	if err != nil {
		return nil, err
	}
	tout, err := c.run(ctx, c.Bin, c.args("tasks", "ls", "-q")...)
	if err != nil {
		return nil, err
	}
	running := map[string]bool{}
	for _, name := range strings.Fields(string(tout)) {
		running[name] = true
	}
	var items []Container
	for _, name := range strings.Fields(string(out)) {
		if !strings.HasPrefix(name, "tessera_") {
			continue
		}
		c := Container{ID: name, Name: name, Running: running[name]}
		if !c.Running {
			c.ExitCode = 1
		}
		items = append(items, c)
	}
	return items, nil
}

func (c *CTR) Logs(ctx context.Context, id string) (string, error) {
	b, err := os.ReadFile(filepath.Join(c.LogDir, id+".log"))
	if err != nil {
		return "", nil
	}
	return string(b), nil
}

func (c *CTR) Prune(ctx context.Context) error {
	_, err := c.run(ctx, c.Bin, c.args("content", "prune")...)
	return err
}

func (c *CTR) HasImage(ctx context.Context, ref string) (bool, error) {
	out, err := c.run(ctx, c.Bin, c.args("images", "ls", "-q")...)
	if err != nil {
		return false, err
	}
	return imageListed(string(out), ref), nil
}

func (c *CTR) ExportImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	if err := os.MkdirAll(c.LogDir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(c.LogDir, "export-*.tar")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	out, err := c.run(ctx, c.Bin, c.args("images", "export", path, ctrRef(ref))...)
	if err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("ctr export: %s", out)
	}
	f, err := os.Open(path)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &tempImage{File: f, path: path}, nil
}

func (c *CTR) ImportImage(ctx context.Context, ref string, r io.Reader) error {
	if err := os.MkdirAll(c.LogDir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(c.LogDir, "import-*.tar")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	out, err := c.run(ctx, c.Bin, c.args("images", "import", path)...)
	if err != nil {
		return fmt.Errorf("ctr import: %s", out)
	}
	return nil
}

type tempImage struct {
	*os.File
	path string
}

func (t *tempImage) Close() error {
	err := t.File.Close()
	removeErr := os.Remove(t.path)
	if err != nil {
		return err
	}
	return removeErr
}

func ctrRef(image string) string {
	if image == "" {
		return image
	}
	if !strings.Contains(image, ".") && !strings.Contains(image, "/") {
		return "docker.io/library/" + image
	}
	return image
}

func imageListed(out, ref string) bool {
	want := ctrRef(ref)
	for _, line := range strings.Fields(out) {
		if line == want || line == ref {
			return true
		}
		if strings.TrimSuffix(line, ":latest") == strings.TrimSuffix(want, ":latest") {
			return true
		}
	}
	return false
}

func hostPort(spec Spec) int {
	if len(spec.Ports) == 0 {
		return 0
	}
	if spec.Ports[0].Host != 0 {
		return spec.Ports[0].Host
	}
	return spec.Ports[0].Container
}
