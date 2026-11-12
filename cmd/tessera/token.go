package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tessera/internal/api"
	"tessera/internal/client"
	"tessera/internal/config"
)

func cmdToken(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tessera token join|gateway|revoke|rotate|recover")
	}
	cl, err := openClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fs := flag.NewFlagSet("token "+args[0], flag.ContinueOnError)
	node := fs.String("node", "", "node ID")
	route := fs.String("route", "", "gateway Route")
	ttl := fs.String("ttl", "10m", "join token lifetime")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("token command takes flags only")
	}
	switch args[0] {
	case "join":
		token, err := cl.JoinToken(ctx, *node, *ttl)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	case "gateway":
		token, err := cl.GatewayToken(ctx, *route)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	case "revoke":
		return cl.RevokeNode(ctx, *node)
	case "rotate", "recover":
		path := filepath.Join(config.Dir(), "pending-api-token")
		var token string
		if args[0] == "rotate" {
			if err := os.MkdirAll(config.Dir(), 0700); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return fmt.Errorf("resolve any existing pending token with tessera token recover: %w", err)
			}
			token = api.NewID() + api.NewID() + api.NewID() + api.NewID()
			_, err = f.WriteString(token)
			if err == nil {
				err = f.Sync()
			}
			f.Close()
			if err != nil {
				return err
			}
			err = cl.RotateToken(ctx, token)
			if err != nil {
				var response *client.HTTPError
				if errors.As(err, &response) && response.Status >= 400 && response.Status < 500 {
					probe := client.New(cl.Endpoint(), token)
					probe.SetControllers(cl.Controllers())
					if _, newErr := probe.ControllerStatus(ctx); newErr != nil {
						if _, oldErr := cl.ControllerStatus(ctx); oldErr == nil {
							os.Remove(path)
							return err
						}
					}
				}
			}
		} else {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			token = strings.TrimSpace(string(body))
		}
		probe := client.New(cl.Endpoint(), token)
		probe.SetControllers(cl.Controllers())
		if _, err := probe.ControllerStatus(ctx); err != nil {
			return fmt.Errorf("rotation not verified; preserve %s and retry tessera token recover: %w", path, err)
		}
		if err := config.Save(config.Dir(), config.File{URL: probe.Endpoint(), Controllers: probe.Controllers(), Token: token}); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		fmt.Println("API token rotated; saved client configuration updated. Update any TESSERA_TOKEN overrides. Controller replication tokens are unchanged.")
		return nil
	default:
		return fmt.Errorf("unknown token command %q", args[0])
	}
}
