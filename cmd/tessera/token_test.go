package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"tessera/internal/client"
	"tessera/internal/config"
	"tessera/internal/controller"
	"tessera/internal/store"
)

func TestTokenRotationKeepsPendingKeyWhenCommittedResponseIsLost(t *testing.T) {
	data := t.TempDir()
	t.Setenv("TESSERA_DATA", data)
	t.Setenv("TESSERA_TOKEN", "")
	t.Setenv("TESSERA_URL", "")
	st, err := store.Open(filepath.Join(data, "tessera.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := controller.New(st, controller.Config{DataDir: data, Token: "replication-root"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	handler := s.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tokens/rotate" {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code != 200 {
				t.Errorf("rotate commit: %d %s", recorder.Code, recorder.Body.String())
			}
			http.Error(w, "old credential rejected after lost response", 401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	if err := config.Save(data, config.File{URL: server.URL, Token: s.Token}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "token"), []byte(s.Token), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmdToken([]string{"rotate"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(data)
	if err != nil || cfg.Token == s.Token {
		t.Fatalf("new API credential was lost: %+v %v", cfg, err)
	}
	if _, err := client.New(server.URL, cfg.Token).ControllerStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	root, err := os.ReadFile(filepath.Join(data, "token"))
	if err != nil || string(root) != s.Token {
		t.Fatal("rotation changed the replication root")
	}
	if _, err := os.Stat(filepath.Join(data, "pending-api-token")); !os.IsNotExist(err) {
		t.Fatal("verified rotation left an unresolved pending credential")
	}
}

func TestControllerRestartPreservesRotatedAPIKeyAndReplicationRoot(t *testing.T) {
	data := t.TempDir()
	t.Setenv("TESSERA_TOKEN", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, admin, err := startControllerConfig(ctx, "127.0.0.1:0", data, controller.Config{Token: "original-root"})
	if err != nil {
		t.Fatal(err)
	}
	rotated := "rotated-administrator-key-12345678901234567890"
	if err := admin.RotateToken(ctx, rotated); err != nil {
		s.Close()
		s.Store.Close()
		t.Fatal(err)
	}
	if err := config.Save(data, config.File{URL: admin.Endpoint(), Token: rotated}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(data, "token")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TESSERA_TOKEN", rotated)
	restarted, local, err := startController(ctx, "127.0.0.1:0", data)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Store.Close()
	defer restarted.Close()
	if local.Token != rotated || restarted.Token != "original-root" {
		t.Fatal("restart confused the API key with the immutable replication root")
	}
	checkInstalledFile(t, filepath.Join(data, "token"), "original-root\n", 0600)
	if _, err := local.ListApps(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load(data)
	if err != nil || saved.Token != rotated {
		t.Fatal("service startup overwrote the saved rotated key")
	}
	if _, err := client.New(local.Endpoint(), "original-root").ListApps(ctx); err == nil {
		t.Fatal("restart restored access for the revoked API key")
	}
	if _, err := controller.New(restarted.Store, controller.Config{DataDir: data, Token: rotated}); err == nil {
		t.Fatal("controller silently replaced its stored replication root")
	}
}
