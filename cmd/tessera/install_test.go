package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"tessera/internal/config"
)

func TestInstallUser(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home with & spaces")
			source := filepath.Join(t.TempDir(), "downloaded-tessera")
			if err := os.WriteFile(source, []byte("binary-v1"), 0o755); err != nil {
				t.Fatal(err)
			}
			opts := installOptions{
				Data: filepath.Join(home, "data"), BinDir: filepath.Join(home, "bin"),
				URL: "http://127.0.0.1:7468", Token: "private-cluster-token", Runtime: "fake", Labels: "fabric=east",
				Env: map[string]string{"PATH": "/usr/bin:/bin", "DOCKER_HOST": "unix:///tmp/docker.sock"},
			}
			var calls [][]string
			run := func(name string, args ...string) error {
				calls = append(calls, append([]string{name}, args...))
				if len(args) > 0 && args[0] == "print" {
					return errors.New("not loaded")
				}
				return nil
			}
			var out bytes.Buffer
			if err := installUser(goos, home, source, 501, opts, run, &out); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(opts.BinDir, "tessera")
			checkInstalledFile(t, bin, "binary-v1", 0o755)
			checkInstalledFile(t, filepath.Join(opts.Data, "token"), opts.Token+"\n", 0o600)
			cfg, err := config.Load(opts.Data)
			if err != nil || cfg.URL != opts.URL || cfg.Token != opts.Token {
				t.Fatalf("client config %+v: %v", cfg, err)
			}
			info, err := os.Stat(filepath.Join(opts.Data, "client.json"))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("client config permissions: %v, %v", info, err)
			}
			path, _ := userService(goos, home, bin, opts)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body)+out.String(), opts.Token) {
				t.Fatal("token exposed in service or output")
			}
			if goos == "darwin" {
				if runtime.GOOS == "darwin" {
					if output, err := exec.Command("/usr/bin/plutil", "-lint", "--", path).CombinedOutput(); err != nil {
						t.Fatalf("invalid plist: %v: %s", err, output)
					}
				}
				decoder := xml.NewDecoder(bytes.NewReader(body))
				var values []string
				for {
					tok, err := decoder.Token()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if start, ok := tok.(xml.StartElement); ok && start.Name.Local == "string" {
						var value string
						if err := decoder.DecodeElement(&value, &start); err != nil {
							t.Fatal(err)
						}
						values = append(values, value)
					}
				}
				if len(values) < 8 || !reflect.DeepEqual(values[:8], []string{"tessera", bin, "agent", "--data", filepath.Join(opts.Data, "agent"), "--runtime", "fake", "--url"}) {
					t.Fatalf("plist arguments %q", values)
				}
				want := [][]string{{"launchctl", "print", "gui/501/tessera"}, {"launchctl", "enable", "gui/501/tessera"}, {"launchctl", "bootstrap", "gui/501", path}, {"launchctl", "kickstart", "gui/501/tessera"}}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("service calls %q", calls)
				}
			} else {
				if !strings.Contains(string(body), `"agent" "--data" "`+filepath.Join(opts.Data, "agent")+`"`) || !strings.Contains(string(body), "Restart=on-failure\n") {
					t.Fatalf("unit: %s", body)
				}
				want := [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "tessera.service"}, {"systemctl", "--user", "restart", "tessera.service"}}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("service calls %q", calls)
				}
			}
			opts.Token, opts.URL, opts.NoStart = "", "", true
			calls = nil
			if err := os.Chmod(filepath.Join(opts.Data, "token"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("binary-v2"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := installUser(goos, home, source, 501, opts, run, &out); err != nil {
				t.Fatal(err)
			}
			checkInstalledFile(t, bin, "binary-v2", 0o755)
			checkInstalledFile(t, filepath.Join(opts.Data, "token"), cfg.Token+"\n", 0o600)
			if len(calls) != 0 {
				t.Fatal("no-start invoked service manager")
			}
			if err := installUser(goos, home, bin, 501, opts, run, &out); err != nil {
				t.Fatal(err)
			}
			checkInstalledFile(t, bin, "binary-v2", 0o755)
		})
	}
}

func TestInstallRejectsInvalidSettingsBeforeWriting(t *testing.T) {
	for _, kind := range []string{"platform", "token", "url", "runtime", "labels", "control"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			opts := installOptions{Data: filepath.Join(home, "data"), BinDir: filepath.Join(home, "bin"), Token: "token", Runtime: "fake"}
			goos := "linux"
			switch kind {
			case "platform":
				goos = "windows"
			case "token":
				opts.Token = ""
			case "url":
				opts.URL = "file:///tmp/controller"
			case "runtime":
				opts.Runtime = "invalid"
			case "labels":
				opts.Labels = "fabric"
			case "control":
				opts.Data += "\n[Service]"
			}
			if err := installUser(goos, home, "missing", 501, opts, nil, io.Discard); err == nil {
				t.Fatal("accepted invalid settings")
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("wrote files: %v, %v", entries, err)
			}
		})
	}
}

func TestServiceUpgradeAndFailure(t *testing.T) {
	var calls []string
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	if err := startUserService("darwin", "/tmp/tessera.plist", 501, run); err != nil {
		t.Fatal(err)
	}
	if calls[1] != "launchctl bootout gui/501/tessera" {
		t.Fatalf("did not unload old service: %q", calls)
	}
	for _, goos := range []string{"darwin", "linux"} {
		count := 0
		failure := errors.New("service manager unavailable")
		err := startUserService(goos, "/tmp/tessera.plist", 501, func(string, ...string) error {
			count++
			return failure
		})
		if !errors.Is(err, failure) || (goos == "linux" && count != 1) || (goos == "darwin" && count != 2) {
			t.Fatalf("%s swallowed failure: %v, %d calls", goos, err, count)
		}
	}
}

func TestSystemdLiteralSettings(t *testing.T) {
	value := `/tmp/a "$HOME" 100%\folder`
	if got := systemdQuote(value, true); got != `"/tmp/a \"$$HOME\" 100%%\\folder"` {
		t.Fatalf("command quoting %s", got)
	}
	if got := systemdQuote(value, false); got != `"/tmp/a \"$HOME\" 100%%\\folder"` {
		t.Fatalf("environment quoting %s", got)
	}
}

func checkInstalledFile(t *testing.T, path, want string, mode os.FileMode) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil || string(body) != want {
		t.Fatalf("%s contents %q: %v", path, body, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("%s permissions: %v, %v", path, info, err)
	}
}
