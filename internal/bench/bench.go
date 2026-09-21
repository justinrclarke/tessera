package bench

import (
	"math"
	"os"
	"path/filepath"
	"time"

	"tessera/internal/api"
)

func Quick(dir string) api.Perf {
	cpuStart := time.Now()
	var n uint64
	var x uint64 = 1
	for time.Since(cpuStart) < 30*time.Millisecond {
		x = x*1664525 + 1013904223
		n++
	}
	cpu := finite(float64(n) / seconds(time.Since(cpuStart)))

	buf := make([]byte, 1<<20)
	memStart := time.Now()
	for i := 0; i < 16; i++ {
		copy(buf, buf)
	}
	mem := finite(16 / seconds(time.Since(memStart)))
	return api.Perf{CPU: cpu, Memory: mem, Disk: finite(disk(dir))}
}

func seconds(d time.Duration) float64 {
	s := d.Seconds()
	if s <= 0 {
		return 1e-6
	}
	return s
}

func finite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

func disk(dir string) float64 {
	if dir == "" {
		return 0
	}
	buf := make([]byte, 1<<20)
	path := filepath.Join(dir, ".bench")
	start := time.Now()
	if err := os.WriteFile(path, append(buf, buf...), 0o644); err != nil {
		return 0
	}
	defer os.Remove(path)
	sec := time.Since(start).Seconds()
	if sec <= 0 {
		return 0
	}
	return 2 / sec
}
