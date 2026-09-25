package gpu

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"tessera/internal/api"
)

type Run func(context.Context, string, ...string) ([]byte, error)

func Detect(ctx context.Context, run Run) ([]api.GPU, error) {
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}
	raw, err := run(ctx, "nvidia-smi", "--query-gpu=uuid,name,memory.total,memory.free", "--format=csv,noheader,nounits")
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

func Parse(raw []byte) ([]api.GPU, error) {
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	var out []api.GPU
	seen := map[string]bool{}
	for _, row := range rows {
		if len(row) != 4 {
			return nil, fmt.Errorf("unexpected nvidia-smi GPU row %q", row)
		}
		total, err := strconv.ParseInt(strings.TrimSpace(row[2]), 10, 64)
		if err != nil {
			return nil, err
		}
		free, err := strconv.ParseInt(strings.TrimSpace(row[3]), 10, 64)
		if err != nil {
			return nil, err
		}
		if total < 0 || free < 0 || free > total || total > (1<<63-1)/(1<<20) {
			return nil, fmt.Errorf("invalid GPU memory in %q", row)
		}
		id := strings.TrimSpace(row[0])
		model := strings.TrimSpace(row[1])
		if id == "" || model == "" || seen[id] {
			return nil, fmt.Errorf("invalid GPU identity in %q", row)
		}
		seen[id] = true
		out = append(out, api.GPU{UUID: id, Model: model, MemoryTotal: total << 20, MemoryFree: free << 20})
	}
	return out, nil
}
