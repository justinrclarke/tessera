package k8sbridge

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

const workloadsJSON = `{"items":[
  {"kind":"Pod","metadata":{"namespace":"demo","name":"web-1","labels":{"app":"web"}},"spec":{"nodeName":"worker"},"status":{"phase":"Pending","containerStatuses":[{"restartCount":2,"state":{"waiting":{"reason":"ImagePullBackOff","message":"image unavailable"}}}]}},
  {"kind":"Deployment","metadata":{"namespace":"demo","name":"web"},"spec":{"replicas":2,"selector":{"matchLabels":{"app":"web"}},"template":{"spec":{"containers":[{"name":"web","image":"nginx:bad"}]}}},"status":{"readyReplicas":0,"conditions":[{"type":"Available","status":"False","reason":"MinimumReplicasUnavailable","message":"no ready pods"}]}}
]}`

const nodesJSON = `{"items":[{"kind":"Node","metadata":{"name":"worker"},"status":{"capacity":{"cpu":"4","memory":"8Gi"},"conditions":[{"type":"Ready","status":"True"}]}}]}`
const eventsJSON = `{"items":[{"type":"Warning","metadata":{"namespace":"demo"},"reason":"Failed","message":"image pull failed","involvedObject":{"kind":"Pod","name":"web-1"}}]}`

func fixture() *Bridge {
	return &Bridge{Kubeconfig: "/tmp/test-config", Context: "kind-test", Namespace: "demo", Run: func(_ context.Context, args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--kubeconfig /tmp/test-config --context kind-test") {
			return nil, fmt.Errorf("missing connection flags: %s", joined)
		}
		switch {
		case strings.Contains(joined, "get deployments,statefulsets,daemonsets,jobs,pods -n demo -o json"):
			return []byte(workloadsJSON), nil
		case strings.Contains(joined, "get nodes -o json"):
			return []byte(nodesJSON), nil
		case strings.Contains(joined, "get events -n demo -o json"):
			return []byte(eventsJSON), nil
		case strings.Contains(joined, "logs -n demo pod/web-1 --all-containers=true --tail=200"):
			return []byte("sample log\n"), nil
		default:
			return nil, fmt.Errorf("unexpected kubectl call: %s", joined)
		}
	}}
}

func TestInspectAndExplain(t *testing.T) {
	b := fixture()
	snap, err := b.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Workloads) != 1 || len(snap.Workloads[0].Pods) != 1 || snap.Workloads[0].Pods[0].Reason != "ImagePullBackOff: image unavailable" {
		t.Fatalf("workloads: %+v", snap.Workloads)
	}
	if len(snap.Nodes) != 1 || !snap.Nodes[0].Ready {
		t.Fatalf("nodes: %+v", snap.Nodes)
	}
	findings := snap.Findings()
	if len(findings) != 1 || !strings.Contains(findings[0].Summary, "ImagePullBackOff") {
		t.Fatalf("findings: %+v", findings)
	}
	if got := snap.Explain("why is demo/web down?"); !strings.Contains(got, "ImagePullBackOff") {
		t.Fatalf("explain: %s", got)
	}
	if got, err := b.Logs(context.Background(), "demo/web"); err != nil || got != "sample log\n" {
		t.Fatalf("logs %q: %v", got, err)
	}
}

func TestWorkloadNameAmbiguity(t *testing.T) {
	snap := Snapshot{Workloads: []Workload{{Namespace: "one", Name: "web"}, {Namespace: "two", Name: "web"}}}
	if _, err := snap.Workload("web"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity, got %v", err)
	}
	if got, err := snap.Workload("two/web"); err != nil || got.Namespace != "two" {
		t.Fatalf("qualified lookup %+v: %v", got, err)
	}
}
