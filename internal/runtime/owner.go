package runtime

import (
	"context"
	"tessera/internal/api"
)

type ownedRuntime struct {
	Runtime
	owner  string
	legacy map[string]bool
}

func ForOwner(r Runtime, owner string, assignments []api.Assignment) Runtime {
	legacy := map[string]bool{}
	for _, a := range assignments {
		if a.NodeID == owner {
			legacy["tessera_"+a.ID] = true
		}
	}
	return &ownedRuntime{Runtime: r, owner: owner, legacy: legacy}
}
func (r *ownedRuntime) Start(ctx context.Context, spec Spec) (Container, error) {
	spec.Owner = r.owner
	return r.Runtime.Start(ctx, spec)
}
func (r *ownedRuntime) List(ctx context.Context) ([]Container, error) {
	items, err := r.Runtime.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Container
	for _, item := range items {
		if item.Owner == r.owner || (item.Owner == "" && r.legacy[item.Name]) {
			out = append(out, item)
		}
	}
	return out, nil
}

func (d *Docker) Kind() string { return "docker" }
func (c *CTR) Kind() string    { return "ctr" }
func (f *Fake) Kind() string   { return "fake" }
