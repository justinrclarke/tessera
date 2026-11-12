package main

import (
	"context"
	"flag"
	"fmt"

	"tessera/internal/api"
)

func cmdDelete(args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	kind := fs.String("kind", api.KindApp, "App or Node")
	reason := fs.String("reason", "operator requested removal", "proposal reason")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := fs.Arg(0)
	if fs.NArg() == 2 && (name == "app" || name == "node") {
		if name == "app" {
			*kind = api.KindApp
		} else {
			*kind = "Node"
		}
		name = fs.Arg(1)
	} else if fs.NArg() != 1 {
		return fmt.Errorf("usage: tessera delete app NAME | node ID | [--kind App|Node] NAME")
	}
	if *kind != api.KindApp && *kind != "Node" {
		return fmt.Errorf("delete kind must be App or Node")
	}
	cl, err := openClient()
	if err != nil {
		return err
	}
	id, err := cl.ProposeDelete(context.Background(), *kind, name, *reason)
	if err != nil {
		return err
	}
	fmt.Printf("proposed delete %s/%s\ninspect: tessera confirm\nrun: tessera confirm %s\n", *kind, name, id)
	return nil
}
