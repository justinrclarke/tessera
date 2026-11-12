//go:build darwin || linux

package agent

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockRuntimeFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
