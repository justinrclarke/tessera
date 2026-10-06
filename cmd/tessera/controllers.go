package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"tessera/internal/client"
)

func getControllers(ctx context.Context, cl *client.Client, out io.Writer) error {
	urls := cl.Controllers()
	if len(urls) == 0 {
		discoverCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := cl.ControllerStatus(discoverCtx)
		cancel()
		if err != nil {
			return err
		}
		urls = cl.Controllers()
		if len(urls) == 0 {
			urls = []string{cl.Endpoint()}
		}
	}
	fmt.Fprintf(out, "%-20s %-12s %-8s %-8s %-12s %-12s %s\n", "ID", "ROLE", "WRITE", "EPOCH", "COMMIT", "APPLIED", "URL")
	reachable := 0
	for _, endpoint := range urls {
		probe := client.New(endpoint, cl.Token)
		probe.HTTP = cl.HTTP
		probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		status, err := probe.ControllerStatus(probeCtx)
		cancel()
		if err != nil {
			fmt.Fprintf(out, "%-20s %-12s %-8s %-8s %-12s %-12s %s\n", "?", "unreachable", "false", "?", "?", "?", endpoint)
			fmt.Fprintf(out, "  %s\n", err)
			continue
		}
		reachable++
		fmt.Fprintf(out, "%-20s %-12s %-8t %-8d %-12d %-12d %s\n", status.ID, status.Role, status.Writable, status.Epoch, status.CommitIndex, status.AppliedIndex, endpoint)
		if status.Error != "" {
			fmt.Fprintf(out, "  %s\n", status.Error)
		}
	}
	if reachable == 0 {
		return fmt.Errorf("no controller reachable")
	}
	return nil
}
