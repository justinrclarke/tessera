package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"tessera/internal/config"
)

type installOptions struct {
	Data    string
	BinDir  string
	URL     string
	Token   string
	Runtime string
	Labels  string
	NoStart bool
	Env     map[string]string
}

type serviceRun func(string, ...string) error

func cmdInstall(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var opts installOptions
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.StringVar(&opts.Data, "data", config.Dir(), "cluster data directory")
	fs.StringVar(&opts.BinDir, "bin-dir", filepath.Join(home, ".local", "bin"), "binary installation directory")
	fs.StringVar(&opts.URL, "url", os.Getenv("TESSERA_URL"), "controller URL; discovered over mDNS if empty")
	fs.StringVar(&opts.Token, "token", os.Getenv("TESSERA_TOKEN"), "cluster token; defaults to saved token")
	fs.StringVar(&opts.Runtime, "runtime", "auto", "container runtime: auto, docker, ctr, or fake")
	fs.StringVar(&opts.Labels, "labels", "", "comma-separated node labels")
	fs.BoolVar(&opts.NoStart, "no-start", false, "install files without starting the service")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("install takes flags only")
	}
	opts.Env = map[string]string{"PATH": os.Getenv("PATH")}
	if value := os.Getenv("DOCKER_HOST"); value != "" {
		opts.Env["DOCKER_HOST"] = value
	}
	source, err := os.Executable()
	if err != nil {
		return err
	}
	return installUser(runtime.GOOS, home, source, os.Getuid(), opts, runService, os.Stdout)
}

func installUser(goos, home, source string, uid int, opts installOptions, run serviceRun, out io.Writer) error {
	if goos != "darwin" && goos != "linux" {
		return fmt.Errorf("install is not implemented for %s", goos)
	}
	var err error
	opts.Data, err = filepath.Abs(opts.Data)
	if err != nil {
		return err
	}
	opts.BinDir, err = filepath.Abs(opts.BinDir)
	if err != nil {
		return err
	}
	if _, err := parseLabels(opts.Labels); err != nil {
		return err
	}
	switch opts.Runtime {
	case "auto", "docker", "ctr", "fake":
	default:
		return fmt.Errorf("unknown runtime %q", opts.Runtime)
	}
	cfg, err := config.Load(opts.Data)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read saved client: %w", err)
	}
	if opts.URL == "" {
		opts.URL = cfg.URL
	}
	if opts.Token == "" {
		opts.Token = cfg.Token
	}
	if opts.Token == "" {
		body, err := os.ReadFile(filepath.Join(opts.Data, "token"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		opts.Token = strings.TrimSpace(string(body))
	}
	if opts.Token == "" {
		return fmt.Errorf("cluster token required: pass --token or start a controller with tessera up first")
	}
	if opts.URL != "" {
		for _, endpoint := range strings.Split(opts.URL, ",") {
			u, err := url.Parse(endpoint)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return fmt.Errorf("controller URL must be an http or https URL without embedded credentials")
			}
		}
	}
	bin := filepath.Join(opts.BinDir, "tessera")
	if opts.Env == nil {
		opts.Env = map[string]string{}
	}
	opts.Env["TESSERA_DATA"] = opts.Data
	values := []string{home, bin, opts.Data, opts.URL, opts.Token, opts.Labels}
	for _, v := range opts.Env {
		values = append(values, v)
	}
	for _, value := range values {
		if strings.ContainsFunc(value, unicode.IsControl) {
			return fmt.Errorf("service paths and settings cannot contain control characters")
		}
	}
	path, body := userService(goos, home, bin, opts)
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := installFile(bin, f, 0o755); err != nil {
		return err
	}
	cfgBody, err := json.MarshalIndent(config.File{URL: opts.URL, Token: opts.Token, Controllers: cfg.Controllers}, "", "  ")
	if err != nil {
		return err
	}
	if err := installFile(filepath.Join(opts.Data, "client.json"), bytes.NewReader(cfgBody), 0o600); err != nil {
		return err
	}
	if err := installFile(filepath.Join(opts.Data, "token"), strings.NewReader(opts.Token+"\n"), 0o600); err != nil {
		return err
	}
	if err := installFile(path, strings.NewReader(body), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "installed %s\nservice %s\n", bin, path)
	if opts.NoStart {
		fmt.Fprintln(out, "service has not been started")
		return nil
	}
	if err := startUserService(goos, path, uid, run); err != nil {
		return fmt.Errorf("files installed, but service startup failed: %w", err)
	}
	fmt.Fprintln(out, "agent service started")
	return nil
}

func userService(goos, home, bin string, opts installOptions) (string, string) {
	args := []string{bin, "agent", "--data", filepath.Join(opts.Data, "agent"), "--runtime", opts.Runtime}
	if opts.URL != "" {
		args = append(args, "--url", opts.URL)
	}
	if opts.Labels != "" {
		args = append(args, "--labels", opts.Labels)
	}
	keys := make([]string, 0, len(opts.Env))
	for key := range opts.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	if goos == "darwin" {
		b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict>\n<key>Label</key><string>tessera</string>\n<key>ProgramArguments</key><array>")
		for _, arg := range args {
			b.WriteString("<string>" + xmlText(arg) + "</string>")
		}
		b.WriteString("</array>\n<key>EnvironmentVariables</key><dict>")
		for _, key := range keys {
			b.WriteString("<key>" + xmlText(key) + "</key><string>" + xmlText(opts.Env[key]) + "</string>")
		}
		b.WriteString("</dict>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n")
		b.WriteString("<key>StandardOutPath</key><string>" + xmlText(filepath.Join(opts.Data, "agent.log")) + "</string>\n")
		b.WriteString("<key>StandardErrorPath</key><string>" + xmlText(filepath.Join(opts.Data, "agent.log")) + "</string>\n</dict></plist>\n")
		return filepath.Join(home, "Library", "LaunchAgents", "tessera.plist"), b.String()
	}
	b.WriteString("[Unit]\nDescription=Tessera agent\n[Service]\nExecStart=")
	for i, arg := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(systemdQuote(arg, true))
	}
	b.WriteByte('\n')
	for _, key := range keys {
		b.WriteString("Environment=" + systemdQuote(key+"="+opts.Env[key], false) + "\n")
	}
	b.WriteString("Restart=on-failure\nRestartSec=2\n[Install]\nWantedBy=default.target\n")
	return filepath.Join(home, ".config", "systemd", "user", "tessera.service"), b.String()
}

func startUserService(goos, path string, uid int, run serviceRun) error {
	if goos == "darwin" {
		domain := fmt.Sprintf("gui/%d", uid)
		service := domain + "/tessera"
		if run("launchctl", "print", service) == nil {
			if err := run("launchctl", "bootout", service); err != nil {
				return err
			}
		}
		if err := run("launchctl", "enable", service); err != nil {
			return err
		}
		if err := run("launchctl", "bootstrap", domain, path); err != nil {
			return err
		}
		return run("launchctl", "kickstart", service)
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "enable", "tessera.service"); err != nil {
		return err
	}
	return run("systemctl", "--user", "restart", "tessera.service")
}

func runService(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func installFile(path string, body io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tessera-install-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := io.Copy(f, body); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func xmlText(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func systemdQuote(value string, expand bool) string {
	value = strings.ReplaceAll(value, "%", "%%")
	if expand {
		value = strings.ReplaceAll(value, "$", "$$")
	}
	return strconv.Quote(value)
}
