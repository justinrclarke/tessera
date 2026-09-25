package runtime

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tessera/internal/api"
)

func TestCTRImageArchiveUsesTemporaryFiles(t *testing.T) {
	c, _ := scriptedCTR(t)
	c.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if hasArg(args, "export") {
			path := args[len(args)-2]
			return nil, os.WriteFile(path, []byte("image"), 0o600)
		}
		if hasArg(args, "import") {
			path := args[len(args)-1]
			b, err := os.ReadFile(path)
			if err != nil || string(b) != "image" {
				t.Fatalf("import archive %q: %v", b, err)
			}
		}
		return nil, nil
	}
	r, err := c.ExportImage(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	path := r.(*tempImage).path
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "image" {
		t.Fatalf("export archive %q: %v", b, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("export file remains: %v", err)
	}
	if err := c.ImportImage(context.Background(), "nginx", strings.NewReader("image")); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(c.LogDir, "import-*.tar"))
	if err != nil || len(files) != 0 {
		t.Fatalf("import files %v: %v", files, err)
	}
}

func TestCTRSkipsPullWhenPresent(t *testing.T) {
	c, cmds := scriptedCTR(t)
	c.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		*cmds = append(*cmds, args)
		if hasArg(args, "ls") {
			return []byte("docker.io/library/nginx:latest\n"), nil
		}
		return nil, nil
	}
	got, err := c.Start(context.Background(), Spec{
		Name: "tessera_a", Image: "nginx", Ports: []api.Port{{Container: 80}},
		Env: map[string]string{"Z": "1", "A": "2"}, Resources: api.Resources{Memory: 32 << 20, CPU: 500},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.HostPort != 80 || !got.Running {
		t.Fatalf("%+v", got)
	}
	for _, args := range *cmds {
		if hasArg(args, "pull") {
			t.Fatalf("pulled %v", *cmds)
		}
	}
	run := (*cmds)[len(*cmds)-1]
	if !hasArg(run, "run") || !hasArg(run, "--net-host") || !hasArg(run, "-n") || !hasArg(run, "tessera") {
		t.Fatalf("run %v", run)
	}
	if !hasArg(run, "--memory-limit") || !hasArg(run, "--env") {
		t.Fatalf("flags %v", run)
	}
}

func TestCTRPullsWhenMissing(t *testing.T) {
	c, cmds := scriptedCTR(t)
	c.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		*cmds = append(*cmds, args)
		if hasArg(args, "pull") {
			return []byte("denied"), os.ErrPermission
		}
		return nil, nil
	}
	_, err := c.Start(context.Background(), Spec{Name: "tessera_a", Image: "nginx"})
	se, ok := err.(*StartError)
	if !ok || !strings.Contains(se.Reason, "pull:") {
		t.Fatalf("%v", err)
	}
}

func TestCTRListAndStop(t *testing.T) {
	c, cmds := scriptedCTR(t)
	c.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		*cmds = append(*cmds, args)
		if hasArg(args, "containers") && hasArg(args, "ls") {
			return []byte("tessera_a other tessera_b\n"), nil
		}
		if hasArg(args, "tasks") && hasArg(args, "ls") {
			return []byte("tessera_a\n"), nil
		}
		return nil, nil
	}
	items, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("%+v", items)
	}
	running := map[string]bool{}
	for _, it := range items {
		running[it.Name] = it.Running
	}
	if !running["tessera_a"] || running["tessera_b"] {
		t.Fatalf("%+v", items)
	}
	*cmds = nil
	if err := c.Stop(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("stopped foreign %v", *cmds)
	}
	if err := c.Stop(context.Background(), "tessera_a"); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, args := range *cmds {
		kinds = append(kinds, args[len(args)-2]+" "+args[len(args)-1])
	}
	got := strings.Join(kinds, ",")
	if got != "kill tessera_a,delete tessera_a,delete tessera_a" {
		t.Fatalf("%s", got)
	}
}

func TestCTRLogs(t *testing.T) {
	c, _ := scriptedCTR(t)
	if err := os.WriteFile(filepath.Join(c.LogDir, "tessera_a.log"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := c.Logs(context.Background(), "tessera_a")
	if err != nil || got != "hello" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestCTRMissing(t *testing.T) {
	c, cmds := scriptedCTR(t)
	c.look = func(string) error { return os.ErrNotExist }
	_, err := c.Start(context.Background(), Spec{Name: "tessera_a", Image: "nginx"})
	if err == nil || !strings.Contains(err.Error(), "ctr not found") {
		t.Fatalf("%v", err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("ran %v", *cmds)
	}
}

func TestCTRRejectsGPUWorkloadBeforeStarting(t *testing.T) {
	c, cmds := scriptedCTR(t)
	_, err := c.Start(context.Background(), Spec{Name: "tessera_gpu", Image: "model", GPUs: 1})
	if err == nil || !strings.Contains(err.Error(), "NVIDIA Container Toolkit") {
		t.Fatalf("unexpected GPU error: %v", err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("started unsupported workload: %v", *cmds)
	}
}

func scriptedCTR(t *testing.T) (*CTR, *[][]string) {
	t.Helper()
	var cmds [][]string
	c := NewCTR("/sock")
	c.LogDir = t.TempDir()
	c.look = func(string) error { return nil }
	c.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmds = append(cmds, args)
		return nil, nil
	}
	return c, &cmds
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
