//go:build darwin

package host

import "golang.org/x/sys/unix"

func memory() (int64, int64) {
	totalU, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, 0
	}
	total := int64(totalU)
	pages, err1 := unix.SysctlUint64("vm.page_free_count")
	pageSize, err2 := unix.SysctlUint64("hw.pagesize")
	if err1 != nil || err2 != nil {
		return total, total
	}
	return total, int64(pages * pageSize)
}
