package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

func (a *Agent) acquireRuntimeLock(ctx context.Context) (func(), error) {
	identity, ok := a.Runtime.(interface {
		LockIdentity(context.Context) (string, error)
	})
	if !ok {
		return func() {}, nil
	}
	id, err := identity.LockIdentity(ctx)
	if err != nil {
		return nil, err
	}
	dir := a.LockDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "tessera-runtime-locks")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(id))
	file, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%x.lock", sum)), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockRuntimeFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("another agent may already own this runtime: %w", err)
	}
	return func() { file.Close() }, nil
}
