package k8simport

import (
	"fmt"
	"strings"
	"testing"
)

const manifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: web
        image: nginx:1.25
        ports:
        - containerPort: 80
        resources:
          requests:
            cpu: 100m
            memory: 128Mi
---
apiVersion: v1
kind: Service
metadata:
  name: web
spec:
  ports:
  - port: 80
    targetPort: 80
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: tick
`

func TestConvertDeployment(t *testing.T) {
	got, err := Convert([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Objects) != 2 {
		t.Fatalf("objects %+v skipped %v", got.Objects, got.Skipped)
	}
	app := got.Objects[0].App
	if app == nil || app.Name != "web" || app.Replicas != 2 || app.Image != "nginx:1.25" || app.Resources.CPU != 100 || app.Resources.Memory != 128<<20 {
		t.Fatalf("%+v", app)
	}
	if got.Objects[1].Route == nil || got.Objects[1].Route.Port != 80 {
		t.Fatalf("%+v", got.Objects[1])
	}
	if len(got.Skipped) != 1 {
		t.Fatalf("skipped %v", got.Skipped)
	}
}

func TestFromClusterReportsUnsupportedResources(t *testing.T) {
	res, err := FromClusterOptions(ClusterOptions{Context: "kind-test", Run: func(args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "get deployments,statefulsets,daemonsets,jobs,services,ingresses,configmaps,secrets -A -o json"):
			return []byte(`{"items":[{"kind":"StatefulSet","metadata":{"name":"db"},"spec":{"template":{"spec":{"containers":[{"image":"postgres:17"}]}}}}]}`), nil
		case strings.Contains(joined, "get all -A -o json"):
			return []byte(`{"items":[{"kind":"Pod","metadata":{"name":"db-0"}},{"kind":"ReplicaSet","metadata":{"name":"web-rs"}}]}`), nil
		case strings.Contains(joined, "get customresourcedefinitions,mutatingwebhookconfigurations,validatingwebhookconfigurations -o json"):
			return []byte(`{"items":[{"kind":"CustomResourceDefinition","metadata":{"name":"widgets.example.test"}},{"kind":"MutatingWebhookConfiguration","metadata":{"name":"injector"}}]}`), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s", joined)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 1 || res.Objects[0].App == nil || res.Objects[0].App.Name != "db" {
		t.Fatalf("objects: %+v", res.Objects)
	}
	if len(res.Skipped) != 4 || !strings.Contains(res.Skipped[0], "Pod: 1") || !strings.Contains(res.Skipped[1], "ReplicaSet: 1") || !strings.Contains(res.Skipped[2], "widgets.example.test") || !strings.Contains(res.Skipped[3], "injector") {
		t.Fatalf("skipped: %v", res.Skipped)
	}
}

func TestConvertAllServicePorts(t *testing.T) {
	res, err := Convert([]byte(`kind: Service
metadata:
  name: web
spec:
  ports:
    - port: 80
      targetPort: 8080
    - port: 443
      targetPort: 8443
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 2 || res.Objects[0].Route.Port != 80 || res.Objects[1].Route.Port != 443 || res.Objects[0].Route.Name == res.Objects[1].Route.Name {
		t.Fatalf("routes: %+v", res.Objects)
	}
}
