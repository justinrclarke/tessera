package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/diagnose"
)

func TestAskAndDiagnoseExplainPlacementWithoutMutations(t *testing.T) {
	srv, cl := standaloneClient(t)
	ctx := context.Background()
	if err := cl.Apply(ctx, []byte("kind: Model\nname: model\nimage: inference\ngpus: 1\n---\nkind: Model\nname: model-api\nimage: inference\ngpus: 1\ngpu_model: H100\ngpu_memory: 8Gi\n")); err != nil {
		t.Fatal(err)
	}
	text, err := cl.Ask(ctx, "why is model-api down?")
	if err != nil || !strings.Contains(text, "no nodes have joined") || strings.Contains(text, "model has") || strings.Contains(text, "Nothing is failing") {
		t.Fatalf("missing-node explanation: %s: %v", text, err)
	}
	if _, err := cl.Register(ctx, client.RegisterRequest{ID: "cpu", Capacity: api.Resources{CPU: 4000, Memory: 16 << 30}}); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Register(ctx, client.RegisterRequest{ID: "gpu", Capacity: api.Resources{CPU: 4000, Memory: 16 << 30}, GPUs: 1, GPUInventory: []api.GPU{{UUID: "device", Model: "A100", MemoryFree: 20 << 30}}}); err != nil {
		t.Fatal(err)
	}
	before, _ := srv.Store.ListAssignments()
	actionsBefore, _ := srv.Store.ListActions(100)
	text, err = cl.Ask(ctx, "why is model-api down?")
	if err != nil || !strings.Contains(text, "cpu: needs 1 GPUs; 0 available") || !strings.Contains(text, "gpu: needs 1 GPUs; 0 available") || strings.Contains(text, "model has") {
		t.Fatalf("reserved GPU explanation: %s: %v", text, err)
	}
	raw, err := cl.Diagnose(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var findings []diagnose.Finding
	if err := json.Unmarshal(raw, &findings); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range findings {
		if finding.Class == "placement" && finding.Target == "model-api" {
			found = true
			if finding.Action != "" {
				t.Fatalf("placement explanation proposed an action: %+v", finding)
			}
		}
	}
	after, _ := srv.Store.ListAssignments()
	actionsAfter, _ := srv.Store.ListActions(100)
	if !found || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(actionsBefore, actionsAfter) {
		t.Fatalf("diagnostics changed state or omitted placement: findings=%+v before=%+v after=%+v actions=%+v", findings, before, after, actionsAfter)
	}
	text, err = cl.Ask(ctx, "why is model down?")
	if err != nil || strings.Contains(text, "model-api has") {
		t.Fatalf("unrelated app findings leaked into focused answer: %s: %v", text, err)
	}
}

func TestAskExplainsMissingConfigurationWithoutSecretValues(t *testing.T) {
	srv, cl := standaloneClient(t)
	ctx := context.Background()
	if err := srv.Store.PutApp(api.App{Kind: api.KindApp, Name: "legacy", Image: "web", Replicas: 1, Configs: []string{"missing-settings"}, Secrets: []string{"missing-credentials"}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Store.PutSecret(api.Secret{Kind: api.KindSecret, Name: "other", Data: map[string]string{"PASSWORD": "must-not-appear"}}); err != nil {
		t.Fatal(err)
	}
	text, err := cl.Ask(ctx, "why is legacy down?")
	if err != nil || !strings.Contains(text, "missing Config/missing-settings") || !strings.Contains(text, "missing Secret/missing-credentials") || strings.Contains(text, "must-not-appear") {
		t.Fatalf("configuration explanation: %s: %v", text, err)
	}
	app, _ := srv.Store.GetApp("legacy")
	app.EnvResolved, app.ReleaseEnv = true, map[string]string{"PASSWORD": "must-not-appear"}
	if err := srv.Store.PutApp(app); err != nil {
		t.Fatal(err)
	}
	text, err = cl.Ask(ctx, "why is legacy down?")
	if err != nil || strings.Contains(text, "cannot resolve") || strings.Contains(text, "must-not-appear") || !strings.Contains(text, "no nodes have joined") {
		t.Fatalf("immutable resolved environment treated as missing: %s: %v", text, err)
	}
}

func TestPlacementFindingsNeverEnterHealingActions(t *testing.T) {
	srv, cl := standaloneClient(t)
	ctx := context.Background()
	if _, err := cl.Register(ctx, client.RegisterRequest{ID: "worker", Capacity: api.Resources{CPU: 1000, Memory: 1 << 30}}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Apply(ctx, []byte("kind: App\nname: web\nimage: web\nresources: {cpu: 2}\n")); err != nil {
		t.Fatal(err)
	}
	text, err := cl.Ask(ctx, "why is web down?")
	if err != nil || !strings.Contains(text, "needs 2000m CPU; 1000m available") {
		t.Fatalf("capacity explanation: %s: %v", text, err)
	}
	if err := srv.Reconcile(srv.now()); err != nil {
		t.Fatal(err)
	}
	actions, err := srv.Store.ListActions(100)
	if err != nil || len(actions) != 0 {
		t.Fatalf("read-only placement findings reached healing: %+v: %v", actions, err)
	}
}
