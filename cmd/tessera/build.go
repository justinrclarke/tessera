package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"tessera/internal/fileutil"
	"tessera/internal/stackbuild"
)

func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	input := fs.String("f", "", "App manifest with build inputs")
	output := fs.String("o", "", "write a deployable release manifest")
	archive := fs.String("archive", "", "optional Docker image archive for offline transfer")
	timeout := fs.Duration("timeout", 15*time.Minute, "build and archive deadline")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *output == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: tessera build -f app.yaml -o release.yaml [--archive images.tar]")
	}
	if *timeout <= 0 {
		return fmt.Errorf("build timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	body, err := readFile(*input)
	if err != nil {
		return err
	}
	prepared, images, err := stackbuild.Prepare(ctx, *input, body, nil)
	if err != nil {
		return err
	}
	if len(images) == 0 {
		return fmt.Errorf("manifest has no build stanza")
	}
	if *archive != "" {
		for _, ref := range images {
			if strings.Contains(ref, "@") {
				return fmt.Errorf("offline archives require build.push: false so docker load restores the exact manifest reference")
			}
		}
		dir := filepath.Dir(*archive)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, ".tessera-image-*")
		if err != nil {
			return err
		}
		path := f.Name()
		f.Close()
		defer os.Remove(path)
		args := append([]string{"save", "-o", path}, images...)
		if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("archive images: %s: %w", out, err)
		}
		if err := os.Chmod(path, 0600); err != nil {
			return err
		}
		if err := os.Rename(path, *archive); err != nil {
			return err
		}
	}
	if err := fileutil.Write(*output, prepared, 0600); err != nil {
		return err
	}
	fmt.Println(*output)
	return nil
}
