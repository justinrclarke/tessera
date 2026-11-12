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
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"tessera/internal/config"
)

type installOptions struct {
	Name         string
	Restricted   bool
	Role         string
	ID           string
	Listen       string
	RaftListen   string
	Peers        string
	Bootstrap    bool
	AdvertiseURL string
	Addr         string
	Route        string
	Port         int
	Data         string
	BinDir       string
	URL          string
	Token        string
	Runtime      string
	Labels       string
	NoStart      bool
	Env          map[string]string
}

type serviceRun func(string, ...string) error

func cmdInstall(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var opts installOptions
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.StringVar(&opts.Role, "role", "agent", "agent, controller, or gateway")
	fs.StringVar(&opts.Name, "name", "", "optional unique user-service name")
	fs.BoolVar(&opts.Restricted, "restricted", false, "use scoped node credentials")
	fs.StringVar(&opts.ID, "id", "", "stable controller ID")
	fs.StringVar(&opts.Listen, "listen", ":7468", "controller HTTP listen address")
	fs.StringVar(&opts.RaftListen, "raft-listen", "", "controller replication listen address")
	fs.StringVar(&opts.Peers, "peers", "", "controller peer list")
	fs.BoolVar(&opts.Bootstrap, "bootstrap", false, "bootstrap an empty replica store")
	fs.StringVar(&opts.AdvertiseURL, "advertise-url", "", "reachable controller HTTP URL")
	fs.StringVar(&opts.Addr, "addr", "", "reachable agent address")
	fs.StringVar(&opts.Route, "route", "", "gateway Route name")
	fs.IntVar(&opts.Port, "port", 8080, "gateway listen port")
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
	if opts.Role == "" {
		opts.Role = "agent"
	}
	if opts.Role != "agent" && opts.Role != "controller" && opts.Role != "gateway" {
		return fmt.Errorf("install role must be agent, controller, or gateway")
	}
	if opts.Name != "" && !regexp.MustCompile(`^tessera-[a-zA-Z0-9_-]+$`).MatchString(opts.Name) {
		return fmt.Errorf("service name must start with tessera- and contain only letters, digits, underscores, or hyphens")
	}
	if opts.Role == "gateway" && (!regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(opts.Route) || opts.Port < 1 || opts.Port > 65535) {
		return fmt.Errorf("gateway installation requires a simple Route name and valid port")
	}
	if opts.Role == "controller" {
		if opts.Listen == "" {
			opts.Listen = ":7468"
		}
		if opts.Peers != "" {
			peers, err := parseControllerPeers(opts.Peers)
			if err != nil {
				return err
			}
			found := false
			for _, p := range peers {
				found = found || p.ID == opts.ID
			}
			if !found || opts.RaftListen == "" {
				return fmt.Errorf("controller replicas require id and raft-listen")
			}
		} else if opts.AdvertiseURL == "" {
			return fmt.Errorf("standalone controller installation requires advertise-url")
		}
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
	values := []string{home, bin, opts.Data, opts.URL, opts.Token, opts.Labels, opts.ID, opts.Listen, opts.RaftListen, opts.Peers, opts.AdvertiseURL, opts.Addr, opts.Route}
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
	if _, err := os.Stat(filepath.Join(opts.Data, "token")); os.IsNotExist(err) {
		_, databaseErr := os.Stat(filepath.Join(opts.Data, "tessera.db"))
		if databaseErr != nil && !os.IsNotExist(databaseErr) {
			return databaseErr
		}
		if opts.Role != "controller" || os.IsNotExist(databaseErr) {
			if err := installFile(filepath.Join(opts.Data, "token"), strings.NewReader(opts.Token+"\n"), 0o600); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	} else if err := os.Chmod(filepath.Join(opts.Data, "token"), 0o600); err != nil {
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
	fmt.Fprintf(out, "%s service started\n", opts.Role)
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
	if opts.Addr != "" {
		args = append(args, "--addr", opts.Addr)
	}
	if opts.Role == "agent" || opts.Role == "" {
		if opts.ID != "" {
			args = append(args, "--id", opts.ID)
		}
		if opts.Restricted {
			args = append(args, "--restricted")
		}
	}
	label, logName := "tessera", "agent"
	if opts.Role == "controller" {
		label, logName = "tessera-controller", "controller"
		args = []string{bin, "controller", "--data", opts.Data, "--listen", opts.Listen}
		if opts.ID != "" {
			args = append(args, "--id", opts.ID)
		}
		if opts.Peers != "" {
			args = append(args, "--peers", opts.Peers, "--raft-listen", opts.RaftListen)
		}
		if opts.Bootstrap {
			args = append(args, "--bootstrap")
		}
		if opts.AdvertiseURL != "" {
			args = append(args, "--advertise-url", opts.AdvertiseURL)
		}
	}
	if opts.Role == "gateway" {
		label, logName = "tessera-gateway-"+opts.Route, "gateway-"+opts.Route
		args = []string{bin, "gateway", "--route", opts.Route, "--port", strconv.Itoa(opts.Port)}
	}
	if opts.Name != "" {
		label = opts.Name
	}
	keys := make([]string, 0, len(opts.Env))
	for key := range opts.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	if goos == "darwin" {
		b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict>\n<key>Label</key><string>" + label + "</string>\n<key>ProgramArguments</key><array>")
		for _, arg := range args {
			b.WriteString("<string>" + xmlText(arg) + "</string>")
		}
		b.WriteString("</array>\n<key>EnvironmentVariables</key><dict>")
		for _, key := range keys {
			b.WriteString("<key>" + xmlText(key) + "</key><string>" + xmlText(opts.Env[key]) + "</string>")
		}
		b.WriteString("</dict>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n")
		b.WriteString("<key>StandardOutPath</key><string>" + xmlText(filepath.Join(opts.Data, logName+".log")) + "</string>\n")
		b.WriteString("<key>StandardErrorPath</key><string>" + xmlText(filepath.Join(opts.Data, logName+".log")) + "</string>\n</dict></plist>\n")
		return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), b.String()
	}
	role := opts.Role
	if role == "" {
		role = "agent"
	}
	b.WriteString("[Unit]\nDescription=Tessera " + role + "\n[Service]\nExecStart=")
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
	return filepath.Join(home, ".config", "systemd", "user", label+".service"), b.String()
}

func startUserService(goos, path string, uid int, run serviceRun) error {
	label := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if goos == "darwin" {
		domain := fmt.Sprintf("gui/%d", uid)
		service := domain + "/" + label
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
	if err := run("systemctl", "--user", "enable", label+".service"); err != nil {
		return err
	}
	return run("systemctl", "--user", "restart", label+".service")
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
