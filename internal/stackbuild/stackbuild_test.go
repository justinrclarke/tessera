package stackbuild

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareBuildsAndPinsPublishedImage(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := []byte("kind: App\nname: web\nbuild:\n  base: python:3.13-alpine\n  repository: registry.example/web\n  package_manager: apk\n  packages: [curl]\n  command: [python, app.py]\n  push: true\n")
	var commands []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "docker" {
			t.Fatalf("command %s", name)
		}
		commands = append(commands, strings.Join(args, " "))
		switch args[0] {
		case "build":
			dockerfile, err := os.ReadFile(args[2])
			if err != nil {
				t.Fatal(err)
			}
			if args[len(args)-1] != dir || !strings.Contains(string(dockerfile), "RUN apk add --no-cache curl") || !strings.Contains(string(dockerfile), `CMD ["python","app.py"]`) {
				t.Fatalf("build %v with %s", args, dockerfile)
			}
		case "image":
			if args[1] == "inspect" {
				return []byte("sha256:" + strings.Repeat("a", 64) + "\n"), nil
			}
		case "push":
			return []byte("latest: digest: sha256:" + strings.Repeat("b", 64) + " size: 100"), nil
		}
		return nil, nil
	}
	prepared, images, err := Prepare(context.Background(), manifest, body, run)
	want := "registry.example/web@sha256:" + strings.Repeat("b", 64)
	if err != nil || len(images) != 1 || images[0] != want || !strings.Contains(string(prepared), want) || strings.Contains(string(prepared), "build:") {
		t.Fatalf("prepared %s, images %v: %v", prepared, images, err)
	}
	if len(commands) != 5 || !strings.HasPrefix(commands[0], "build ") || !strings.HasPrefix(commands[3], "push ") {
		t.Fatalf("Docker commands: %v", commands)
	}
}

func TestPrepareLeavesOrdinaryManifestUntouched(t *testing.T) {
	body := []byte("kind: App\nname: web\nimage: nginx:alpine\n")
	got, images, err := Prepare(context.Background(), "app.yaml", body, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("ran Docker for ordinary App")
		return nil, nil
	})
	if err != nil || len(images) != 0 || string(got) != string(body) {
		t.Fatalf("%q %v %v", got, images, err)
	}
}

func TestPrepareRejectsInvalidBuildBeforeDocker(t *testing.T) {
	body := []byte("kind: App\nname: web\nbuild:\n  base: alpine:3\n  repository: registry.example/web\n  packages: [curl]\n  command: [sh]\n")
	_, _, err := Prepare(context.Background(), "app.yaml", body, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("ran Docker for invalid build")
		return nil, nil
	})
	if err == nil {
		t.Fatal("accepted packages without a package manager")
	}
}

func TestPrepareValidatesAppBeforeDocker(t *testing.T) {
	body := []byte("kind: App\nbuild:\n  base: alpine:3\n  repository: registry.example/web\n  command: [sh]\n")
	_, _, err := Prepare(context.Background(), "app.yaml", body, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("ran Docker for App without a name")
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestDockerBuildSmoke(t *testing.T) {
	if os.Getenv("TESSERA_DOCKER_BUILD_SMOKE") == "" {
		t.Skip("set TESSERA_DOCKER_BUILD_SMOKE=1 to use a local Docker daemon")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.txt"), []byte("tessera\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	spec := Spec{Base: "alpine:3.21", Repository: "tessera-build-smoke", Command: []string{"cat", "index.txt"}}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	image, err := spec.Build(context.Background(), filepath.Join(dir, "app.yaml"), run)
	if err != nil {
		t.Fatal(err)
	}
	defer run(context.Background(), "docker", "image", "rm", image)
	out, err := run(context.Background(), "docker", "run", "--rm", image)
	if err != nil || strings.TrimSpace(string(out)) != "tessera" {
		t.Fatalf("built container output %q: %v", out, err)
	}
}

func TestDockerRegistryPushSmoke(t *testing.T) {
	if os.Getenv("TESSERA_DOCKER_REGISTRY_SMOKE") == "" {
		t.Skip("set TESSERA_DOCKER_REGISTRY_SMOKE=1 to use a local Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	out, err := run(ctx, "docker", "run", "--rm", "-d", "-p", "127.0.0.1::5000", "registry:3.1")
	if err != nil {
		t.Fatalf("start registry: %v: %s", err, out)
	}
	container := strings.TrimSpace(string(out))
	defer run(context.Background(), "docker", "rm", "-f", container)
	out, err = run(ctx, "docker", "port", container, "5000/tcp")
	if err != nil {
		t.Fatalf("registry port: %v: %s", err, out)
	}
	address := strings.TrimSpace(string(out))
	if !strings.HasPrefix(address, "127.0.0.1:") {
		t.Fatalf("unexpected registry address %q", address)
	}
	ready := false
	for i := 0; i < 30; i++ {
		resp, err := http.Get("http://" + address + "/v2/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Fatal("registry did not become ready")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.txt"), []byte("published\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := fmt.Sprintf("%s/tessera-stackbuild-smoke", address)
	spec := Spec{Base: "alpine:3.21", Repository: repository, Command: []string{"cat", "index.txt"}, Push: true}
	image, err := spec.Build(ctx, filepath.Join(dir, "app.yaml"), run)
	if err != nil || !strings.HasPrefix(image, repository+"@sha256:") {
		t.Fatalf("published image %q: %v", image, err)
	}
	defer run(context.Background(), "docker", "image", "rm", image)
	imageID, err := run(ctx, "docker", "image", "inspect", "--format={{.Id}}", image)
	if err != nil {
		t.Fatalf("inspect published image: %v: %s", err, imageID)
	}
	tag := repository + ":sha256-" + strings.TrimPrefix(strings.TrimSpace(string(imageID)), "sha256:")
	defer run(context.Background(), "docker", "image", "rm", tag)
	if out, err := run(ctx, "docker", "image", "rm", tag); err != nil {
		t.Fatalf("remove local build tag: %v: %s", err, out)
	}
	_, _ = run(ctx, "docker", "image", "rm", image)
	if _, err := run(ctx, "docker", "image", "inspect", image); err == nil {
		t.Fatal("image remained locally before registry pull")
	}
	if out, err := run(ctx, "docker", "pull", image); err != nil {
		t.Fatalf("pull published digest: %v: %s", err, out)
	}
	if out, err := run(ctx, "docker", "run", "--rm", image); err != nil || strings.TrimSpace(string(out)) != "published" {
		t.Fatalf("published container output %q: %v", out, err)
	}
}
