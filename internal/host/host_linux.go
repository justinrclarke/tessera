//go:build linux

package host

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

func memory() (int64, int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var total, free int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 2 {
			continue
		}
		n, err := strconv.ParseInt(fs[1], 10, 64)
		if err != nil {
			continue
		}
		n *= 1024
		switch strings.TrimSuffix(fs[0], ":") {
		case "MemTotal":
			total = n
		case "MemAvailable":
			free = n
		}
	}
	if free == 0 {
		free = total
	}
	return total, free
}
