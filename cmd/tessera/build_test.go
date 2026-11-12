package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tessera/internal/api"
)

func TestBuildOfflineArchiveRestoresManifestReference(t *testing.T) {
	if os.Getenv("TESSERA_DOCKER_BUILD_SMOKE") == "" {
		t.Skip("set TESSERA_DOCKER_BUILD_SMOKE=1 to use a local Docker daemon")
	}
	dir := t.TempDir()
	contextDir := filepath.Join(dir, "context")
	if err := os.Mkdir(contextDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "version"), []byte("offline artifact\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "app.yaml")
	body := "kind: App\nname: offline\nbuild:\n  base: alpine:3.21\n  repository: tessera-offline-" + api.NewID() + "\n  context: ./context\n  command: [cat, version]\n"
	if err := os.WriteFile(manifest, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	release, archive := filepath.Join(dir, "release.yaml"), filepath.Join(dir, "images.tar")
	if err := cmdBuild([]string{"-f", manifest, "-o", release, "--archive", archive, "--timeout", "45s"}); err != nil {
		t.Fatal(err)
	}
	prepared, err := os.ReadFile(release)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := api.DecodeAll(prepared)
	if err != nil || len(objects) != 1 || objects[0].App.Artifact == nil {
		t.Fatalf("release manifest: %s %v", prepared, err)
	}
	ref := objects[0].App.Image
	defer exec.Command("docker", "image", "rm", ref).Run()
	if out, err := exec.Command("docker", "image", "rm", ref).CombinedOutput(); err != nil {
		t.Fatalf("remove local reference: %s %v", out, err)
	}
	if out, err := exec.Command("docker", "load", "-i", archive).CombinedOutput(); err != nil {
		t.Fatalf("load offline package: %s %v", out, err)
	}
	if out, err := exec.Command("docker", "run", "--rm", ref).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "offline artifact" {
		t.Fatalf("offline release failed: %s %v", out, err)
	}
	for _, path := range []string{release, archive} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private artifact permissions: %s %v", path, err)
		}
	}
}
