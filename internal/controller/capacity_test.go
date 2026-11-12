package controller

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/store"
)

func BenchmarkHeartbeatByClusterSize(b *testing.B) {
	for _, size := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("nodes-%d", size), func(b *testing.B) {
			dir := b.TempDir()
			st, err := store.Open(filepath.Join(dir, "tessera.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer st.Close()
			s, err := New(st, Config{DataDir: dir, Token: "benchmark"})
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			h := httptest.NewServer(s.Handler())
			defer h.Close()
			for i := 0; i < size; i++ {
				if err := st.PutNode(api.Node{ID: fmt.Sprintf("node-%d", i), Status: api.NodeReady, LastSeen: time.Now()}); err != nil {
					b.Fatal(err)
				}
			}
			c := client.New(h.URL, "benchmark")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := c.Heartbeat(context.Background(), fmt.Sprintf("node-%d", i%size), client.HeartbeatRequest{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
