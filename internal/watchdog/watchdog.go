package watchdog

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func Run(args []string) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	for {
		cmd := exec.Command(bin, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		cmd.Env = append(os.Environ(), "TESSERA_WATCHED=1")
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		start := time.Now()
		select {
		case err := <-done:
			signal.Stop(sigs)
			if err == nil || time.Since(start) < 500*time.Millisecond {
				return err
			}
			time.Sleep(200 * time.Millisecond)
		case sig := <-sigs:
			signal.Stop(sigs)
			_ = cmd.Process.Signal(sig)
			<-done
			return nil
		}
	}
}
