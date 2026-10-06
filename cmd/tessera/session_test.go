package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"tessera/internal/client"
	"tessera/internal/config"
	"tessera/internal/controller"
	"tessera/internal/store"
)

func TestSessionDispatchAndErrors(t *testing.T) {
	in := strings.NewReader("\nget apps\ntessera apply -f 'my app.yaml'\nwhy isn't web running?\nget nodes\napply -f \"unfinished\nup\napply -f=-\nwhat changed?\nexit\nget apps\n")
	var out, errs bytes.Buffer
	var commands [][]string
	var questions []string
	err := session(in, &out, &errs, func(args []string) error {
		commands = append(commands, args)
		if args[0] == "get" && args[1] == "nodes" {
			return errors.New("controller unavailable")
		}
		return nil
	}, func(q string) error {
		questions = append(questions, q)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"get", "apps"}, {"apply", "-f", "my app.yaml"}, {"get", "nodes"}}
	if !reflect.DeepEqual(commands, want) || !reflect.DeepEqual(questions, []string{"why isn't web running?", "what changed?"}) {
		t.Fatalf("commands %q, questions %q", commands, questions)
	}
	for _, text := range []string{"controller unavailable", "unfinished quote", "separate terminal", "use a file path"} {
		if !strings.Contains(errs.String(), text) {
			t.Fatalf("missing %s in %s", text, errs.String())
		}
	}
}

func TestCommandWords(t *testing.T) {
	for _, tt := range []struct {
		line string
		want []string
	}{
		{`apply -f "my app.yaml"`, []string{"apply", "-f", "my app.yaml"}},
		{`ask "why isn't web up?"`, []string{"ask", "why isn't web up?"}},
		{`apply -f my\ app.yaml`, []string{"apply", "-f", "my app.yaml"}},
		{`ask '$(touch sentinel)'`, []string{"ask", "$(touch sentinel)"}},
		{`ask "say \"hello\"" ''`, []string{"ask", `say "hello"`, ""}},
	} {
		got, err := commandWords(tt.line)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%s: %q, %v", tt.line, got, err)
		}
	}
}

func TestSessionUsesControllerAndLeavesItRunning(t *testing.T) {
	data := t.TempDir()
	t.Setenv("TESSERA_DATA", data)
	t.Setenv("TESSERA_URL", "")
	t.Setenv("TESSERA_TOKEN", "")
	t.Setenv("TESSERA_LLM_URL", "")
	st, err := store.Open(filepath.Join(data, "tessera.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := controller.New(st, controller.Config{DataDir: data, Token: "session-token"})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	defer srv.Close()
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	if err := config.Save(data, config.File{URL: httpServer.URL, Token: srv.Token}); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(data, "my app.yaml")
	if err := os.WriteFile(manifest, []byte("kind: App\nname: web\nimage: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := "apply -f '" + manifest + "'\nget apps\nwhy isn't web running?\nexit\n"
	var prompts, errs bytes.Buffer
	output := captureCLI(t, func() {
		if err := session(strings.NewReader(input), &prompts, &errs, run, func(q string) error { return cmdAsk([]string{q}) }); err != nil {
			t.Fatal(err)
		}
	})
	if errs.Len() != 0 || !strings.Contains(output, "web image=nginx") || !strings.Contains(output, "ready=0/1") {
		t.Fatalf("session output %s, errors %s", output, errs.String())
	}
	cl := client.New(httpServer.URL, srv.Token)
	if err := cl.Health(context.Background()); err != nil {
		t.Fatalf("session stopped cluster: %v", err)
	}
	app, err := cl.GetApp(context.Background(), "web")
	if err != nil || app.Generation != 1 {
		t.Fatalf("session apply %+v: %v", app, err)
	}
}

func TestOpenClientFromEnvironment(t *testing.T) {
	t.Setenv("TESSERA_DATA", t.TempDir())
	t.Setenv("TESSERA_URL", "http://127.0.0.1:7468")
	t.Setenv("TESSERA_TOKEN", "token")
	cl, err := openClient()
	if err != nil || cl.Base != "http://127.0.0.1:7468" || cl.Token != "token" {
		t.Fatalf("environment client %+v: %v", cl, err)
	}
}

func TestInstalledAgentConfiguration(t *testing.T) {
	for _, savedURL := range []bool{true, false} {
		name := "save_selected_address"
		if savedURL {
			name = "use_saved_address_with_token_file"
		}
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			t.Setenv("TESSERA_DATA", data)
			t.Setenv("TESSERA_TOKEN", "")
			st, err := store.Open(filepath.Join(data, "tessera.db"))
			if err != nil {
				t.Fatal(err)
			}
			srv, err := controller.New(st, controller.Config{DataDir: data, Token: "agent-token"})
			if err != nil {
				st.Close()
				t.Fatal(err)
			}
			defer srv.Close()
			httpServer := httptest.NewServer(srv.Handler())
			defer httpServer.Close()
			cfg := config.File{Token: srv.Token}
			if savedURL {
				cfg.URL = httpServer.URL
			}
			if err := config.Save(data, cfg); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(data, "token"), []byte(srv.Token), 0o600); err != nil {
				t.Fatal(err)
			}
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "-test.run=^TestAgentCLIHelper$")
			cmd.Env = append(os.Environ(), "TESSERA_CLI_AGENT_HELPER=1")
			if !savedURL {
				cmd.Env = append(cmd.Env, "TESSERA_CLI_AGENT_URL="+httpServer.URL)
			}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = cmd.Process.Signal(syscall.SIGTERM)
				if err := cmd.Wait(); err != nil {
					t.Errorf("agent CLI: %v: %s", err, output.String())
				}
			}()
			cl := client.New(httpServer.URL, srv.Token)
			for {
				nodes, err := cl.ListNodes(ctx)
				if err == nil && len(nodes) == 1 && nodes[0].ID == "installed-node" {
					break
				}
				if ctx.Err() != nil {
					t.Fatal("installed agent did not join its configured controller")
				}
				time.Sleep(20 * time.Millisecond)
			}
			cfg, err = config.Load(data)
			if err != nil || cfg.URL != httpServer.URL || cfg.Token != srv.Token {
				t.Fatalf("agent client config %+v: %v", cfg, err)
			}
		})
	}
}

func TestAgentCLIHelper(t *testing.T) {
	if os.Getenv("TESSERA_CLI_AGENT_HELPER") != "1" {
		return
	}
	args := []string{"--runtime", "fake", "--id", "installed-node"}
	if selected := os.Getenv("TESSERA_CLI_AGENT_URL"); selected != "" {
		args = append(args, "--url", selected)
	}
	if err := cmdAgent(args); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultCommandOpensSession(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString("version\nquit\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	saved := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = saved }()
	output := captureCLI(t, func() {
		if err := run(nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "Tessera session") || strings.Count(output, "tessera> ") != 2 {
		t.Fatalf("default command output %s", output)
	}
}

func captureCLI(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = saved }()
	fn()
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
