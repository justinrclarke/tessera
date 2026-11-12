package drill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"tessera/internal/agent"
	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/runtime"
)

func placementDiagnostics() error {
	ctx := context.Background()
	_, cl, cleanup := boot(time.Now())
	defer cleanup()
	if err := cl.Apply(ctx, []byte("kind: Model\nname: inference\nimage: inference:local\ngpus: 1\ngpu_model: H100\ngpu_memory: 8Gi\nnode_labels: {storage: shared}\n")); err != nil {
		return err
	}
	expect := func(reason string) error {
		text, err := cl.Ask(ctx, "why is inference down?")
		if err != nil || !strings.Contains(text, reason) || strings.Contains(text, "Nothing is failing") {
			return fmt.Errorf("expected placement reason %q, got %q: %v", reason, text, err)
		}
		assignments, err := cl.ListAssignments(ctx)
		if err != nil || len(assignments) != 0 {
			return fmt.Errorf("non-fitting model was placed: %+v: %v", assignments, err)
		}
		return nil
	}
	if err := expect("no nodes have joined"); err != nil {
		return err
	}
	req := client.RegisterRequest{ID: "model-worker", Addr: "127.0.0.1", GPUs: 1, GPUInventory: []api.GPU{{UUID: "gpu-one", Model: "A100", MemoryFree: 16 << 30}}, Labels: map[string]string{"storage": "local"}}
	if _, err := cl.Register(ctx, req); err != nil {
		return err
	}
	if err := expect("requires node label storage=shared"); err != nil {
		return err
	}
	req.Labels["storage"] = "shared"
	if _, err := cl.Register(ctx, req); err != nil {
		return err
	}
	if err := expect(`of model "H100"`); err != nil {
		return err
	}
	req.GPUInventory[0].Model, req.GPUInventory[0].MemoryFree = "H100", 4<<30
	if _, err := cl.Register(ctx, req); err != nil {
		return err
	}
	if err := expect("free memory per device"); err != nil {
		return err
	}
	req.GPUInventory[0].MemoryFree = 16 << 30
	if _, err := cl.Register(ctx, req); err != nil {
		return err
	}
	dir := tDir()
	defer os.RemoveAll(dir)
	ag := &agent.Agent{ID: req.ID, DataDir: filepath.Join(dir, "agent"), Token: cl.Token, Client: cl, Runtime: runtime.NewFake(), Addr: req.Addr, Labels: req.Labels, GPUProbe: func(context.Context) ([]api.GPU, error) { return req.GPUInventory, nil }}
	if err := ag.Tick(ctx); err != nil {
		return err
	}
	assignments, err := cl.ListAssignments(ctx)
	if err != nil || len(assignments) != 1 || assignments[0].Status != api.StatusRunning || len(assignments[0].GPUDevices) != 1 || assignments[0].GPUDevices[0] != "gpu-one" {
		return fmt.Errorf("fitting model did not receive its GPU: %+v: %v", assignments, err)
	}
	actionsBefore, err := cl.ListActions(ctx)
	if err != nil {
		return err
	}
	text, err := cl.Ask(ctx, "why is inference down?")
	if err != nil || !strings.Contains(text, "ready=1/1") || strings.Contains(text, "unplaced") {
		return fmt.Errorf("placement finding did not clear: %s: %v", text, err)
	}
	actions, err := cl.ListActions(ctx)
	if err != nil || !reflect.DeepEqual(actionsBefore, actions) {
		return fmt.Errorf("placement diagnostics created actions: %+v: %v", actions, err)
	}
	return nil
}
