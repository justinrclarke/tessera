package runtime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDockerSmoke(t *testing.T) {
	if os.Getenv("TESSERA_DOCKER_SMOKE") == "" {
		t.Skip("set TESSERA_DOCKER_SMOKE=1 to use a local Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d := NewDocker(DockerSock())
	has, err := d.HasImage(ctx, "busybox:latest")
	if err != nil || !has {
		t.Fatalf("busybox image unavailable: %v", err)
	}
	name := "tessera_smoke_" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	c, err := d.Start(ctx, Spec{Name: name, Image: "busybox:latest", Command: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop(context.Background(), c.ID)
	items, err := d.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.ID == c.ID && item.Running {
			found = true
		}
	}
	if !found {
		t.Fatalf("started container not listed: %+v", c)
	}
	r, err := d.ExportImage(ctx, "busybox:latest")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := d.ImportImage(ctx, "busybox:latest", r); err != nil {
		t.Fatal(err)
	}
}
