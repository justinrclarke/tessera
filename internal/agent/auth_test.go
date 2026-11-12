package agent

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"tessera/internal/client"
	"tessera/internal/controller"
	"tessera/internal/runtime"
	"tessera/internal/store"
)

func TestRestrictedAgentRestoresScopedCredentialWithoutPromotion(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := controller.New(st, controller.Config{DataDir: dir, Token: "admin-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	ctx := context.Background()
	admin := client.New(httpServer.URL, srv.Token)
	join, err := admin.JoinToken(ctx, "worker", "10m")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := client.New(httpServer.URL, join)
	runtime := runtime.NewFake()
	a := Agent{ID: "worker", Restricted: true, DataDir: filepath.Join(dir, "agent"), Token: join, Client: bootstrap, Runtime: runtime}
	if err := a.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	cache, err := a.readCache()
	if err != nil || cache.NodeToken == "" || cache.Snapshot.Index != 0 {
		t.Fatalf("scoped cache=%+v err=%v", cache, err)
	}
	if bootstrap.Token != join {
		t.Fatal("agent changed the caller's shared bootstrap client")
	}
	if _, err := a.Client.ListApps(ctx); err == nil {
		t.Fatal("agent retained administrator access")
	}
	restarted := Agent{DataDir: a.DataDir, Token: "expired-bootstrap", Client: client.New(httpServer.URL, "expired-bootstrap"), Runtime: runtime, LostAt: time.Now().Add(-time.Hour)}
	if err := restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if !restarted.Restricted {
		t.Fatal("restart lost scoped role")
	}
	if promoted, err := restarted.maybePromote(ctx); err != nil || promoted {
		t.Fatalf("scoped worker promoted: %v %v", promoted, err)
	}
}
