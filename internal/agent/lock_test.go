package agent

import (
	"context"
	"tessera/internal/api"
	"tessera/internal/runtime"
	"testing"
)

type lockedRuntime struct {
	*runtime.Fake
	identity string
}

func (r *lockedRuntime) LockIdentity(context.Context) (string, error) { return r.identity, nil }

func TestDuplicateAgentRuntimeLockReleasesAfterExit(t *testing.T) {
	rtm := &lockedRuntime{Fake: runtime.NewFake(), identity: api.NewID()}
	a, b := Agent{Runtime: rtm}, Agent{Runtime: rtm}
	unlock, err := a.acquireRuntimeLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second, err := b.acquireRuntimeLock(context.Background()); err == nil {
		second()
		unlock()
		t.Fatal("duplicate runtime agent acquired ownership")
	}
	unlock()
	second, err := b.acquireRuntimeLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second()
}
