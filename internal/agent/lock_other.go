//go:build !darwin && !linux

package agent

import (
	"fmt"
	"os"
)

func lockRuntimeFile(*os.File) error {
	return fmt.Errorf("runtime ownership locking is unsupported on this platform")
}
