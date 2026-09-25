package mcp

import (
	"context"
	"encoding/json"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/k8sbridge"
)

type Server struct {
	Client *client.Client
	Bridge *k8sbridge.Bridge
}

func (s *Server) Run(ctx context.Context) error {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "tessera", Version: api.Version}, nil)
	if s.Bridge != nil {
		s.addKubernetes(srv)
		return srv.Run(ctx, &mcpsdk.StdioTransport{})
	}
	add(srv, "list_apps", "List Tessera apps", func(ctx context.Context, _ empty) (string, error) {
		apps, err := s.Client.ListApps(ctx)
		return asJSON(apps, err)
	})
	add(srv, "get_app", "Get one app by name", func(ctx context.Context, in nameIn) (string, error) {
		app, err := s.Client.GetApp(ctx, in.Name)
		return asJSON(app, err)
	})
	add(srv, "list_nodes", "List nodes, including status and performance", func(ctx context.Context, _ empty) (string, error) {
		nodes, err := s.Client.ListNodes(ctx)
		return asJSON(nodes, err)
	})
	add(srv, "get_node", "Get one node by id", func(ctx context.Context, in nameIn) (string, error) {
		n, err := s.Client.GetNode(ctx, in.Name)
		return asJSON(n, err)
	})
	add(srv, "logs", "Logs for an app", func(ctx context.Context, in nameIn) (string, error) {
		return s.Client.Logs(ctx, in.Name)
	})
	add(srv, "diagnose", "Run detectors and return findings. Does not change the cluster.", func(ctx context.Context, _ empty) (string, error) {
		raw, err := s.Client.Diagnose(ctx)
		return string(raw), err
	})
	add(srv, "list_actions", "Recent automatic and proposed actions", func(ctx context.Context, _ empty) (string, error) {
		actions, err := s.Client.ListActions(ctx)
		return asJSON(actions, err)
	})
	add(srv, "explain", "Explain what is wrong and what was already done", func(ctx context.Context, in askIn) (string, error) {
		return s.Client.Ask(ctx, in.Question)
	})
	add(srv, "metrics", "Node scores and assignment counts", func(ctx context.Context, _ empty) (string, error) {
		nodes, err := s.Client.ListNodes(ctx)
		if err != nil {
			return "", err
		}
		asgs, err := s.Client.ListAssignments(ctx)
		if err != nil {
			return "", err
		}
		return asJSON(map[string]any{"nodes": nodes, "assignments": len(asgs)}, nil)
	})
	return srv.Run(ctx, &mcpsdk.StdioTransport{})
}

func (s *Server) addKubernetes(srv *mcpsdk.Server) {
	add(srv, "list_apps", "List Kubernetes workloads without changing them", func(ctx context.Context, _ empty) (string, error) {
		view, err := s.Bridge.Inspect(ctx)
		if err != nil {
			return "", err
		}
		return asJSON(view.Workloads, nil)
	})
	add(srv, "get_app", "Get a Kubernetes workload by namespace/name", func(ctx context.Context, in nameIn) (string, error) {
		view, err := s.Bridge.Inspect(ctx)
		if err != nil {
			return "", err
		}
		app, err := view.Workload(in.Name)
		return asJSON(app, err)
	})
	add(srv, "list_nodes", "List Kubernetes nodes and readiness", func(ctx context.Context, _ empty) (string, error) {
		view, err := s.Bridge.Inspect(ctx)
		if err != nil {
			return "", err
		}
		return asJSON(view.Nodes, nil)
	})
	add(srv, "get_node", "Get a Kubernetes node by name", func(ctx context.Context, in nameIn) (string, error) {
		view, err := s.Bridge.Inspect(ctx)
		if err != nil {
			return "", err
		}
		node, err := view.Node(in.Name)
		return asJSON(node, err)
	})
	add(srv, "logs", "Recent logs for a Kubernetes workload", func(ctx context.Context, in nameIn) (string, error) {
		return s.Bridge.Logs(ctx, in.Name)
	})
	add(srv, "diagnose", "Inspect Kubernetes workload and node availability without changes", func(ctx context.Context, _ empty) (string, error) {
		view, err := s.Bridge.Inspect(ctx)
		if err != nil {
			return "", err
		}
		return asJSON(view.Findings(), nil)
	})
	add(srv, "list_actions", "Kubernetes bridge performs no Tessera actions", func(ctx context.Context, _ empty) (string, error) {
		return asJSON([]api.Action{}, nil)
	})
	add(srv, "explain", "Explain Kubernetes availability from live status", func(ctx context.Context, in askIn) (string, error) {
		view, err := s.Bridge.Inspect(ctx)
		if err != nil {
			return "", err
		}
		return view.Explain(in.Question), nil
	})
	add(srv, "metrics", "Kubernetes node readiness and workload counts", func(ctx context.Context, _ empty) (string, error) {
		view, err := s.Bridge.Inspect(ctx)
		if err != nil {
			return "", err
		}
		ready := 0
		for _, node := range view.Nodes {
			if node.Ready {
				ready++
			}
		}
		return asJSON(map[string]int{"nodes": len(view.Nodes), "nodes_ready": ready, "workloads": len(view.Workloads), "findings": len(view.Findings())}, nil)
	})
}

type empty struct{}

type nameIn struct {
	Name string `json:"name" jsonschema:"app or node name"`
}

type askIn struct {
	Question string `json:"question" jsonschema:"question about the cluster"`
}

type textOut struct {
	Text string `json:"text"`
}

func add[In any](srv *mcpsdk.Server, name, desc string, fn func(context.Context, In) (string, error)) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: name, Description: desc}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, in In) (*mcpsdk.CallToolResult, textOut, error) {
		text, err := fn(ctx, in)
		if err != nil {
			return nil, textOut{}, err
		}
		return nil, textOut{Text: text}, nil
	})
}

func asJSON(v any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
