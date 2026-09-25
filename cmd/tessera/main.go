package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"tessera/internal/agent"
	"tessera/internal/api"
	"tessera/internal/ask"
	"tessera/internal/client"
	"tessera/internal/config"
	"tessera/internal/controller"
	"tessera/internal/discover"
	"tessera/internal/drill"
	"tessera/internal/k8sbridge"
	"tessera/internal/k8simport"
	"tessera/internal/mcp"
	rt "tessera/internal/runtime"
	"tessera/internal/store"
	"tessera/internal/watchdog"

	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage()
		return nil
	case "version":
		fmt.Println(api.Version)
		return nil
	case "up":
		rest := stripFlag(args[1:], "--watched")
		if os.Getenv("TESSERA_WATCHED") != "1" && !hasFlag(args, "--watched") {
			return watchdog.Run(append([]string{"up"}, rest...))
		}
		return cmdUp(rest)
	case "controller":
		return cmdController(args[1:])
	case "agent":
		return cmdAgent(args[1:])
	case "apply":
		return cmdApply(args[1:])
	case "get":
		return cmdGet(args[1:])
	case "confirm":
		return cmdConfirm(args[1:])
	case "ask":
		return cmdAsk(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "import":
		return cmdImport(args[1:])
	case "drill":
		return cmdDrill()
	case "backup":
		return cmdBackup(args[1:])
	case "restore":
		return cmdRestore(args[1:])
	case "watchdog":
		if len(args) < 3 || args[1] != "--" {
			return fmt.Errorf("usage: tessera watchdog -- <args>")
		}
		return watchdog.Run(args[2:])
	case "install":
		return cmdInstall(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Printf(`tessera %s

One binary. Apps, jobs, routes, configs, secrets, and a policy.
The controller places work, heals known failures, and can elect a new leader
from a signed snapshot if the current one dies. Destructive actions stay proposed.

  tessera up [--listen :7468] [--runtime auto|docker|ctr|fake] [--labels KEY=VALUE,...]
  tessera apply -f app.yaml
  tessera get apps|nodes|assignments|actions|routes
  tessera confirm [id]
  tessera ask "why is web down"
  tessera agent [--url http://controller:7468]
  tessera import -f deploy.yaml
  tessera mcp
  tessera backup -o tessera-backup.db
  tessera restore -f tessera-backup.db --data NEW_DIRECTORY
  tessera drill

An agent that already has the token joins over mDNS. No IP required.

  kind: App
  name: web
  image: nginx:alpine
  replicas: 2
  sensitive_to: cpu
`, api.Version)
}

func cmdUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	listen := fs.String("listen", "0.0.0.0:7468", "controller listen address")
	data := fs.String("data", config.Dir(), "data directory")
	runtimeName := fs.String("runtime", "auto", "container runtime: auto, docker, ctr, or fake")
	labelsArg := fs.String("labels", "", "comma-separated node labels")
	if err := fs.Parse(args); err != nil {
		return err
	}
	labels, err := parseLabels(*labelsArg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv, cl, err := startController(ctx, *listen, *data)
	if err != nil {
		return err
	}
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "controller %s  token in %s\n", cl.Base, filepath.Join(*data, "client.json"))
	rtm, err := rt.Open(*runtimeName)
	if err != nil {
		return fmt.Errorf("runtime %s: %w (use --runtime fake to run without containers)", *runtimeName, err)
	}
	ag := &agent.Agent{
		DataDir: filepath.Join(*data, "agent"),
		Token:   cl.Token,
		URL:     cl.Base,
		Client:  cl,
		Runtime: rtm,
		Addr:    advertiseHost(*listen),
		Labels:  labels,
	}
	err = ag.Run(ctx)
	if errors.Is(err, agent.ErrHalted) {
		<-ctx.Done()
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func cmdController(args []string) error {
	fs := flag.NewFlagSet("controller", flag.ContinueOnError)
	listen := fs.String("listen", "0.0.0.0:7468", "listen address")
	data := fs.String("data", config.Dir(), "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv, cl, err := startController(ctx, *listen, *data)
	if err != nil {
		return err
	}
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "controller %s\n", cl.Base)
	<-ctx.Done()
	return nil
}

func cmdAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "controller URL; discovered over mDNS if empty")
	token := fs.String("token", os.Getenv("TESSERA_TOKEN"), "cluster token")
	id := fs.String("id", os.Getenv("TESSERA_NODE_ID"), "stable node ID")
	data := fs.String("data", filepath.Join(config.Dir(), "agent"), "agent data directory")
	runtimeName := fs.String("runtime", "auto", "container runtime")
	labelsArg := fs.String("labels", "", "comma-separated node labels")
	if err := fs.Parse(args); err != nil {
		return err
	}
	labels, err := parseLabels(*labelsArg)
	if err != nil {
		return err
	}
	if *token == "" {
		if b, err := os.ReadFile(filepath.Join(config.Dir(), "token")); err == nil {
			*token = strings.TrimSpace(string(b))
		}
	}
	if *token == "" {
		if cfg, err := config.Load(config.Dir()); err == nil {
			*token = cfg.Token
			if *urlFlag == "" {
				*urlFlag = cfg.URL
			}
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	url := *urlFlag
	if url == "" {
		found, err := discover.Lookup(ctx)
		if err != nil || len(found) == 0 {
			return fmt.Errorf("no controller found on the lan; pass --url")
		}
		best := found[0]
		for _, f := range found[1:] {
			if f.Epoch > best.Epoch {
				best = f
			}
		}
		url = best.URL
		fmt.Fprintf(os.Stderr, "found controller %s epoch %d\n", url, best.Epoch)
	}
	if *token == "" {
		return fmt.Errorf("token required (--token, TESSERA_TOKEN, or ~/.tessera/token)")
	}
	rtm, err := rt.Open(*runtimeName)
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	ag := &agent.Agent{ID: *id, DataDir: *data, Token: *token, URL: url, Client: client.New(url, *token), Runtime: rtm, Labels: labels}
	err = ag.Run(ctx)
	if ctx.Err() != nil || errors.Is(err, agent.ErrHalted) {
		return nil
	}
	return err
}

func cmdApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	file := fs.String("f", "", "yaml file, or - for stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("usage: tessera apply -f app.yaml")
	}
	body, err := readFile(*file)
	if err != nil {
		return err
	}
	cl, err := openClient()
	if err != nil {
		return err
	}
	return cl.Apply(context.Background(), body)
}

func cmdGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: tessera get apps|nodes|assignments|actions|routes")
	}
	cl, err := openClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch fs.Arg(0) {
	case "apps", "app":
		if fs.NArg() == 2 {
			app, err := cl.GetApp(ctx, fs.Arg(1))
			if err != nil {
				return err
			}
			asgs, _ := cl.ListAssignments(ctx)
			fmt.Printf("%s image=%s generation=%d healthy=%d replicas=%d\n", app.Name, app.Image, app.Generation, app.HealthyGeneration, app.Replicas)
			for _, a := range asgs {
				if a.App == app.Name {
					fmt.Printf("  %s node=%s status=%s restarts=%d %s\n", a.ID, a.NodeID, a.Status, a.Restarts, a.Reason)
				}
			}
			return nil
		}
		apps, err := cl.ListApps(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("%-16s %-24s %8s %8s\n", "NAME", "IMAGE", "READY", "GEN")
		asgs, _ := cl.ListAssignments(ctx)
		for _, app := range apps {
			ready := 0
			for _, a := range asgs {
				if a.App == app.Name && a.Status == api.StatusRunning {
					ready++
				}
			}
			fmt.Printf("%-16s %-24s %8d %8d\n", app.Name, app.Image, ready, app.Generation)
		}
	case "nodes", "node":
		nodes, err := cl.ListNodes(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("%-20s %-10s %12s %12s %12s %8s\n", "ID", "STATUS", "CPU", "MEM", "DISK", "GPUS")
		for _, n := range nodes {
			fmt.Printf("%-20s %-10s %12.0f %12.0f %12.0f %8d\n", n.ID, n.Status, n.Perf.CPU, n.Perf.Memory, n.Perf.Disk, n.GPUs)
		}
	case "assignments":
		asgs, err := cl.ListAssignments(ctx)
		if err != nil {
			return err
		}
		for _, a := range asgs {
			fmt.Printf("%s app=%s node=%s status=%s gen=%d epoch=%d\n", a.ID, a.App, a.NodeID, a.Status, a.Generation, a.Epoch)
		}
	case "actions":
		actions, err := cl.ListActions(ctx)
		if err != nil {
			return err
		}
		for _, a := range actions {
			fmt.Printf("%s %s %s %s -> %s\n", a.At.Format(time.RFC3339), a.Kind, a.Target, a.Reason, a.Result)
		}
	case "routes":
		routes, err := cl.ListRoutes(ctx)
		if err != nil {
			return err
		}
		for _, r := range routes {
			fmt.Printf("%s app=%s port=%d\n", r.Name, r.App, r.Port)
		}
	default:
		return fmt.Errorf("unknown resource %q", fs.Arg(0))
	}
	return nil
}

func cmdConfirm(args []string) error {
	fs := flag.NewFlagSet("confirm", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cl, err := openClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if fs.NArg() == 0 {
		actions, err := cl.ListActions(ctx)
		if err != nil {
			return err
		}
		for _, a := range actions {
			if a.Result == "proposed" {
				fmt.Printf("%s %s %s %s\n", a.ID, a.Kind, a.Target, a.Reason)
			}
		}
		return nil
	}
	result, err := cl.Confirm(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Println(result)
	return nil
}

func cmdAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	kubernetes := fs.Bool("kubernetes", false, "inspect the current kubectl context")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig for Kubernetes inspection")
	kubeContext := fs.String("context", "", "Kubernetes context")
	kubeNamespace := fs.String("namespace", "", "Kubernetes namespace; defaults to all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: tessera ask \"why is web down\"")
	}
	var text string
	if *kubernetes || *kubeconfig != "" || *kubeContext != "" || *kubeNamespace != "" {
		view, err := (&k8sbridge.Bridge{Kubeconfig: *kubeconfig, Context: *kubeContext, Namespace: *kubeNamespace}).Inspect(context.Background())
		if err != nil {
			return err
		}
		text = view.Explain(strings.Join(fs.Args(), " "))
	} else {
		cl, err := openClient()
		if err != nil {
			return err
		}
		var errAsk error
		text, errAsk = cl.Ask(context.Background(), strings.Join(fs.Args(), " "))
		if errAsk != nil {
			return errAsk
		}
	}
	note := ask.WithModel(context.Background(), text, ask.OpenAI{
		URL: os.Getenv("TESSERA_LLM_URL"), Key: os.Getenv("TESSERA_LLM_KEY"), Model: os.Getenv("TESSERA_LLM_MODEL"),
	})
	fmt.Print(note)
	return nil
}

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "controller URL")
	token := fs.String("token", "", "cluster token")
	kubernetes := fs.Bool("kubernetes", false, "inspect the current kubectl context")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig for Kubernetes inspection")
	kubeContext := fs.String("context", "", "Kubernetes context")
	kubeNamespace := fs.String("namespace", "", "Kubernetes namespace; defaults to all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *kubernetes || *kubeconfig != "" || *kubeContext != "" || *kubeNamespace != "" {
		return (&mcp.Server{Bridge: &k8sbridge.Bridge{Kubeconfig: *kubeconfig, Context: *kubeContext, Namespace: *kubeNamespace}}).Run(context.Background())
	}
	cl, err := openClient()
	if err != nil && (*urlFlag == "" || *token == "") {
		return err
	}
	if *urlFlag != "" {
		cl = client.New(*urlFlag, *token)
	}
	return (&mcp.Server{Client: cl}).Run(context.Background())
}

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	file := fs.String("f", "", "kubernetes yaml")
	fromCluster := fs.Bool("from-cluster", false, "read the current kubectl context")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig for --from-cluster")
	kubeContext := fs.String("context", "", "Kubernetes context for --from-cluster")
	kubeNamespace := fs.String("namespace", "", "Kubernetes namespace for --from-cluster; defaults to all")
	dry := fs.Bool("dry-run", false, "print Tessera yaml, do not apply")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var res k8simport.Result
	var err error
	switch {
	case *fromCluster:
		res, err = k8simport.FromClusterOptions(k8simport.ClusterOptions{Kubeconfig: *kubeconfig, Context: *kubeContext, Namespace: *kubeNamespace})
	case *file != "":
		body, rerr := readFile(*file)
		if rerr != nil {
			return rerr
		}
		res, err = k8simport.Convert(body)
	default:
		return fmt.Errorf("usage: tessera import -f deploy.yaml | tessera import --from-cluster")
	}
	if err != nil {
		return err
	}
	for _, s := range res.Skipped {
		fmt.Fprintf(os.Stderr, "skipped %s\n", s)
	}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	for _, obj := range res.Objects {
		var v any
		switch {
		case obj.App != nil:
			v = obj.App
		case obj.Route != nil:
			v = obj.Route
		case obj.Config != nil:
			v = obj.Config
		case obj.Secret != nil:
			v = obj.Secret
		default:
			continue
		}
		if err := enc.Encode(v); err != nil {
			return err
		}
	}
	if *dry {
		fmt.Print(buf.String())
		return nil
	}
	cl, err := openClient()
	if err != nil {
		return err
	}
	return cl.Apply(context.Background(), []byte(buf.String()))
}

func cmdDrill() error {
	reports, err := drill.Run()
	for _, r := range reports {
		mark := "pass"
		if !r.Pass {
			mark = "fail"
		}
		fmt.Printf("%s  %s", mark, r.Name)
		if r.Detail != "" {
			fmt.Printf("  %s", r.Detail)
		}
		fmt.Println()
	}
	return err
}

func cmdBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	data := fs.String("data", config.Dir(), "data directory")
	out := fs.String("o", "tessera-backup.db", "destination")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(*data, "tessera.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Backup(*out)
}

func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	data := fs.String("data", config.Dir(), "new controller data directory")
	file := fs.String("f", "", "backup database")
	epoch := fs.Uint64("epoch", 0, "minimum new leader epoch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("usage: tessera restore -f tessera-backup.db --data NEW_DIRECTORY")
	}
	return store.RestoreBackup(*file, filepath.Join(*data, "tessera.db"), *epoch)
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	data := fs.String("data", config.Dir(), "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path := filepath.Join(home, "Library", "LaunchAgents", "tessera.plist")
		body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>tessera</string>
<key>ProgramArguments</key><array><string>%s</string><string>up</string><string>--watched</string><string>--data</string><string>%s</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
</dict></plist>
`, bin, *data)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s\nload it with: launchctl load %s\n", path, path)
		return nil
	case "linux":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path := filepath.Join(home, ".config", "systemd", "user", "tessera.service")
		body := fmt.Sprintf(`[Unit]
Description=Tessera
[Service]
ExecStart=%s up --watched --data %s
Restart=always
[Install]
WantedBy=default.target
`, bin, *data)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s\nenable it with: systemctl --user enable --now tessera\n", path)
		return nil
	default:
		return fmt.Errorf("install is not implemented for %s", runtime.GOOS)
	}
}

func startController(ctx context.Context, listen, data string) (*controller.Server, *client.Client, error) {
	if err := os.MkdirAll(data, 0o755); err != nil {
		return nil, nil, err
	}
	st, err := store.Open(filepath.Join(data, "tessera.db"))
	if err != nil {
		return nil, nil, err
	}
	srv, err := controller.New(st, controller.Config{DataDir: data, Token: os.Getenv("TESSERA_TOKEN")})
	if err != nil {
		return nil, nil, err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, nil, err
	}
	go func() { _ = srv.Serve(ctx, ln) }()
	cl := client.New("http://"+reachable(ln.Addr().String()), srv.Token)
	_ = config.Save(data, config.File{URL: cl.Base, Token: srv.Token})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cl.Health(context.Background()) == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := net.LookupPort("tcp", port)
	if stop, err := discover.Advertise(srv.ID, discover.FirstURL(p), p, srv.Epoch()); err == nil {
		go func() {
			<-ctx.Done()
			_ = stop()
		}()
	}
	if err := os.WriteFile(filepath.Join(data, "token"), []byte(srv.Token+"\n"), 0o600); err != nil {
		return nil, nil, err
	}
	return srv, cl, nil
}

func openClient() (*client.Client, error) {
	cfg, err := config.Load(config.Dir())
	if err != nil {
		return nil, fmt.Errorf("no controller config at %s (run tessera up first)", config.Dir())
	}
	if v := os.Getenv("TESSERA_URL"); v != "" {
		cfg.URL = v
	}
	if v := os.Getenv("TESSERA_TOKEN"); v != "" {
		cfg.Token = v
	}
	return client.New(cfg.URL, cfg.Token), nil
}

func readFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func reachable(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func stripFlag(args []string, flag string) []string {
	var out []string
	for _, a := range args {
		if a != flag {
			out = append(out, a)
		}
	}
	return out
}

func advertiseHost(listen string) string {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		ips := discover.LocalIPs()
		if len(ips) > 0 {
			return ips[0].String()
		}
		return "127.0.0.1"
	}
	return host
}

func parseLabels(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, item := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(item, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("invalid node label %q; use key=value", item)
		}
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("duplicate node label %q", key)
		}
		out[key] = value
	}
	return out, nil
}
