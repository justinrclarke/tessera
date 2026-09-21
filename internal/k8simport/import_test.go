package k8simport

import "testing"

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
