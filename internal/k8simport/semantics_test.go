package k8simport

import (
	"encoding/json"
	"strings"
	"testing"

	"tessera/internal/api"
)

func TestNamespaceSelectorsNamedPortsAndCommand(t *testing.T) {
	input := `
kind: List
items:
- kind: Service
  metadata: {name: frontend, namespace: blue}
  spec: {selector: {tier: web}, ports: [{port: 8080, targetPort: http}]}
- kind: Deployment
  metadata: {name: web, namespace: blue}
  spec:
    replicas: 0
    template:
      metadata: {labels: {tier: web}}
      spec:
        nodeSelector: {disk: fast}
        containers:
        - image: web
          command: [server]
          args: [--listen, '80']
          ports: [{name: http, containerPort: 80}]
          resources: {requests: {cpu: 5m}}
- kind: Deployment
  metadata: {name: web, namespace: green}
  spec:
    template:
      metadata: {labels: {tier: web}}
      spec:
        containers: [{image: other}]
`
	res, err := Convert([]byte(input))
	if err != nil || len(res.Objects) != 3 || len(res.Skipped) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	route, app := res.Objects[0].Route, res.Objects[1].App
	if route.App != "blue.web" || route.TargetPort != 80 || app.Replicas != 0 || app.NodeLabels["disk"] != "fast" || strings.Join(app.Command, " ") != "server --listen 80" {
		t.Fatalf("route=%+v app=%+v", route, app)
	}
	decoded, err := api.DecodeAll([]byte(res.YAML))
	if err != nil || decoded[1].App.Resources.CPU != 5 {
		t.Fatalf("resource quantity changed in output: %+v %v", decoded, err)
	}
}

func TestUnsupportedControllerSemanticsAreReported(t *testing.T) {
	for _, test := range []struct {
		kind string
		spec string
	}{
		{"StatefulSet", ""}, {"DaemonSet", ""},
		{"Job", `"completions":5,"parallelism":2,`},
		{"Job", `"parallelism":2,`},
		{"Deployment", `"replicas":-1,`},
	} {
		t.Run(test.kind+test.spec, func(t *testing.T) {
			input := `{"kind":"` + test.kind + `","metadata":{"name":"work"},"spec":{` + test.spec + `"template":{"spec":{"containers":[{"image":"work"}]}}}}`
			res, err := Convert([]byte(input))
			if err == nil && (len(res.Objects) != 0 || len(res.Skipped) != 1) {
				t.Fatalf("silently changed workload: %+v", res)
			}
		})
	}
}

func TestServiceNeverGuessesBackendOrDropsProtocol(t *testing.T) {
	for _, change := range []string{
		`"selector":{"missing":"label"},"ports":[{"port":80}]`,
		`"ports":[{"port":80}]`,
		`"selector":{"app":"web"},"ports":[{"port":80,"protocol":"UDP"}]`,
		`"selector":{"app":"web"},"ports":[{"port":80,"targetPort":"missing"}]`,
	} {
		input := `[{"kind":"Deployment","metadata":{"name":"web"},"spec":{"template":{"metadata":{"labels":{"app":"web"}},"spec":{"containers":[{"image":"web"}]}}}},{"kind":"Service","metadata":{"name":"web"},"spec":{` + change + `}}]`
		res, err := Convert([]byte(input))
		if err != nil || len(res.Objects) != 1 || len(res.Skipped) != 1 {
			t.Fatalf("%+v %v", res, err)
		}
	}
}

func TestJobCountsAndSecretData(t *testing.T) {
	res, err := Convert([]byte(`
kind: Job
metadata: {name: worker}
spec:
  completions: 3
  parallelism: 3
  template:
    spec: {containers: [{image: worker}]}
---
kind: Secret
metadata: {name: config}
data: {empty: '', password: b2xk}
stringData: {password: new}
`))
	if err != nil || len(res.Objects) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if app := res.Objects[0].App; app.Replicas != 3 || app.Gang {
		t.Fatalf("%+v", app)
	}
	if secret := res.Objects[1].Secret; secret.Data["empty"] != "" || secret.Data["password"] != "new" {
		t.Fatalf("%+v", secret)
	}
	var doc map[string]any
	json.Unmarshal([]byte(`{"kind":"Secret","metadata":{"name":"bad"},"data":{"key":"invalid!"}}`), &doc)
	if _, _, err := one(doc); err == nil {
		t.Fatal("invalid base64 accepted")
	}
}
