//go:build darwin || linux

package host

import (
	"runtime"

	"golang.org/x/sys/unix"
)

func probe(path string) Info {
	info := Info{CPU: runtime.NumCPU()}
	info.MemTotal, info.MemFree = memory()
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err == nil {
		info.DiskTotal = int64(st.Blocks) * int64(st.Bsize)
		info.DiskFree = int64(st.Bavail) * int64(st.Bsize)
	}
	return info
}
