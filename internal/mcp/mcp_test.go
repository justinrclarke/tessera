package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"tessera/internal/k8sbridge"
)

func TestKubernetesToolsAreReadOnlyAndAvailable(t *testing.T) {
	ctx := context.Background()
	bridge := &k8sbridge.Bridge{Run: func(_ context.Context, args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "get deployments,statefulsets,daemonsets,jobs,pods"):
			return []byte(`{"items":[{"kind":"Deployment","metadata":{"namespace":"demo","name":"web"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"web"}}},"status":{"readyReplicas":0}}]}`), nil
		case strings.Contains(joined, "get nodes"):
			return []byte(`{"items":[]}`), nil
		case strings.Contains(joined, "get events"):
			return []byte(`{"items":[]}`), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s", joined)
		}
	}}
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "tessera", Version: "test"}, nil)
	(&Server{Bridge: bridge}).addKubernetes(srv)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"list_apps": true, "get_app": true, "list_nodes": true, "get_node": true, "logs": true, "diagnose": true, "list_actions": true, "explain": true, "metrics": true}
	for _, tool := range listed.Tools {
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
	result, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: "diagnose"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) == 0 {
		t.Fatalf("diagnose result: %+v", result)
	}
}
