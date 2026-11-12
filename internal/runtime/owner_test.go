package runtime

import (
	"context"
	"tessera/internal/api"
	"testing"
)

func TestRuntimeOwnershipOnlyIncludesOwnAndRecordedLegacyContainers(t *testing.T) {
	ctx := context.Background()
	fake := NewFake()
	for _, spec := range []Spec{{Name: "tessera_other", Owner: "other"}, {Name: "tessera_legacy"}, {Name: "tessera_unrecorded"}} {
		if _, err := fake.Start(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	rtm := ForOwner(fake, "me", []api.Assignment{{ID: "legacy", NodeID: "me"}})
	if _, err := rtm.Start(ctx, Spec{Name: "tessera_new"}); err != nil {
		t.Fatal(err)
	}
	items, err := rtm.List(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("owned list %+v %v", items, err)
	}
	for _, item := range items {
		if item.Name == "tessera_other" || item.Name == "tessera_unrecorded" {
			t.Fatal("foreign container claimed")
		}
		if item.Name == "tessera_new" && item.Owner != "me" {
			t.Fatal("owner label not propagated")
		}
	}
}
