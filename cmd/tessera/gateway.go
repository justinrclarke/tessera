package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"tessera/internal/gateway"
)

func cmdGateway(args []string) error {
	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	route := fs.String("route", "", "Route name")
	port := fs.Int("port", 8080, "stable gateway listen port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *route == "" || *port < 1 || *port > 65535 || fs.NArg() != 0 {
		return fmt.Errorf("usage: tessera gateway --route NAME [--port 8080]")
	}
	cl, err := openClient()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	g := &gateway.Gateway{Client: cl, Route: *route, Port: *port, OnError: func(err error) { fmt.Fprintln(os.Stderr, "gateway refresh:", err) }}
	if err := g.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
