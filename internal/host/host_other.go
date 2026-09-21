//go:build !darwin && !linux

package host

import "runtime"

func probe(path string) Info {
	return Info{CPU: runtime.NumCPU()}
}
