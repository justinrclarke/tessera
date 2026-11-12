package drill

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"tessera/internal/agent"
	"tessera/internal/client"
	"tessera/internal/runtime"
)

func scopedCredentials() error {
	ctx := context.Background()
	_, admin, cleanup := boot(time.Now())
	defer cleanup()
	if err := admin.Apply(ctx, []byte("kind: Secret\nname: private\ndata: {PASSWORD: assigned-only}\n---\nkind: App\nname: scoped\nimage: web\nsecrets: [private]\n")); err != nil {
		return err
	}
	join, err := admin.JoinToken(ctx, "worker", "10m")
	if err != nil {
		return err
	}
	dir := tDir()
	defer os.RemoveAll(dir)
	rtm := runtime.NewFake()
	worker := &agent.Agent{ID: "worker", DataDir: dir, Token: join, Client: client.New(admin.Endpoint(), join), Runtime: rtm, Restricted: true}
	if err := worker.Tick(ctx); err != nil {
		return err
	}
	if _, err := worker.Client.Snapshot(ctx); err == nil {
		return fmt.Errorf("scoped worker could read cluster secrets")
	}
	items, err := rtm.List(ctx)
	if err != nil || len(items) != 1 {
		return fmt.Errorf("scoped workload did not run: %+v %v", items, err)
	}
	identity := items[0].ID
	if err := admin.RotateToken(ctx, strings.Repeat("new-admin-key-", 5)); err != nil {
		return err
	}
	if _, err := admin.ListApps(ctx); err == nil {
		return fmt.Errorf("revoked admin credential still works")
	}
	restarted := &agent.Agent{ID: "worker", DataDir: dir, Token: "expired-bootstrap", Client: client.New(admin.Endpoint(), "expired-bootstrap"), Runtime: rtm, Restricted: true}
	if err := restarted.Tick(ctx); err != nil {
		return err
	}
	items, err = rtm.List(ctx)
	if err != nil || len(items) != 1 || items[0].ID != identity {
		return fmt.Errorf("credential rotation or worker restart changed workload identity: %+v %v", items, err)
	}
	return nil
}
