package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
)

func TestNativeMacServiceRecoveryAndReinstall(t *testing.T) {
	if os.Getenv("TESSERA_SERVICE_SMOKE") == "" {
		t.Skip("set TESSERA_SERVICE_SMOKE=1 to exercise a disposable launchd user service")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	build := exec.Command("go", "build", "-o", source, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	ln.Close()
	name := "tessera-accept-" + api.NewID()
	service := fmt.Sprintf("gui/%d/%s", os.Getuid(), name)
	opts := installOptions{Name: name, Role: "controller", Data: filepath.Join(root, "data"), BinDir: filepath.Join(root, "bin"), Token: "disposable-service-token", Runtime: "fake", Listen: address, AdvertiseURL: "http://" + address, Env: map[string]string{"PATH": os.Getenv("PATH")}}
	t.Cleanup(func() {
		if err := runService("launchctl", "bootout", service); err != nil {
			t.Logf("service cleanup: %v", err)
		}
	})
	install := func() {
		t.Helper()
		if err := installUser("darwin", root, source, os.Getuid(), opts, runService, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	pidPattern := regexp.MustCompile(`(?m)^\s*pid = (\d+)`)
	c := client.New("http://"+address, opts.Token)
	wait := func(previous string) string {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			out, err := exec.Command("launchctl", "print", service).CombinedOutput()
			match := pidPattern.FindSubmatch(out)
			if err == nil && len(match) == 2 && string(match[1]) != previous {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, err := c.ListApps(ctx)
				cancel()
				if err == nil {
					return string(match[1])
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		log, _ := os.ReadFile(filepath.Join(opts.Data, "controller.log"))
		t.Fatalf("service did not recover: %s", log)
		return ""
	}
	install()
	first := wait("")
	if err := c.Apply(context.Background(), []byte("kind: App\nname: saved\nimage: busybox:latest\nreplicas: 0\n")); err != nil {
		t.Fatal(err)
	}
	if err := runService("launchctl", "kill", "SIGKILL", service); err != nil {
		t.Fatal(err)
	}
	second := wait(first)
	install()
	wait(second)
	if app, err := c.GetApp(context.Background(), "saved"); err != nil || app.Name != "saved" {
		t.Fatalf("state lost after recovery and reinstall: %+v %v", app, err)
	}
	log, err := os.ReadFile(filepath.Join(opts.Data, "controller.log"))
	if err != nil || !strings.Contains(string(log), "controller ") {
		t.Fatalf("logs unavailable: %v", err)
	}
}
