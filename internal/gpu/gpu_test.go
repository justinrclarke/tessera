package gpu

import (
	"context"
	"strings"
	"testing"
)

func TestDetectParsesNVMLBackedInventory(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "nvidia-smi" || strings.Join(args, " ") != "--query-gpu=uuid,name,memory.total,memory.free --format=csv,noheader,nounits" {
			t.Fatalf("command %s %v", name, args)
		}
		return []byte("GPU-a, NVIDIA A100, 40960, 20000\nGPU-b, NVIDIA H100, 81920, 70000\n"), nil
	}
	items, err := Detect(context.Background(), run)
	if err != nil || len(items) != 2 || items[1].Model != "NVIDIA H100" || items[1].MemoryFree != 70000<<20 {
		t.Fatalf("inventory %+v: %v", items, err)
	}
}

func TestParseRejectsInvalidMemory(t *testing.T) {
	if _, err := Parse([]byte("GPU-a,NVIDIA A100,100,101\n")); err == nil {
		t.Fatal("accepted free memory above total")
	}
}

func TestParseRejectsDuplicateGPU(t *testing.T) {
	if _, err := Parse([]byte("GPU-a,NVIDIA A100,100,50\nGPU-a,NVIDIA A100,100,50\n")); err == nil {
		t.Fatal("accepted duplicate GPU UUID")
	}
}
