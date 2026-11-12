package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"tessera/internal/api"
	"tessera/internal/runtime"
	"tessera/internal/volume"
)

func cmdStorage(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tessera storage backup --app NAME -o FILE | restore -f FILE --directory NEW_DIRECTORY")
	}
	fs := flag.NewFlagSet("storage "+args[0], flag.ContinueOnError)
	app := fs.String("app", "", "persistent App to back up on its Docker host")
	output := fs.String("o", "", "new backup archive")
	input := fs.String("f", "", "backup archive to restore")
	directory := fs.String("directory", "", "new destination directory")
	maxBytes := fs.Int64("max-bytes", 64<<30, "maximum archive or restored data bytes")
	timeout := fs.Duration("timeout", 15*time.Minute, "backup deadline")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *maxBytes <= 0 || *timeout <= 0 {
		return fmt.Errorf("invalid storage arguments")
	}
	switch args[0] {
	case "backup":
		if *app == "" || *output == "" {
			return fmt.Errorf("backup requires --app and -o")
		}
		c, err := openClient()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		assignments, err := c.ListAssignments(ctx)
		if err != nil {
			return err
		}
		var selected []api.Assignment
		for _, assignment := range assignments {
			if assignment.App == *app && api.Active(assignment.Status) {
				selected = append(selected, assignment)
			}
		}
		if len(selected) != 1 || selected[0].Status != api.StatusRunning || selected[0].Storage == nil {
			return fmt.Errorf("backup requires exactly one running persistent assignment")
		}
		file, err := os.CreateTemp(filepath.Dir(*output), ".tessera-storage-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		defer file.Close()
		writer := &boundedArchive{file: file, remaining: *maxBytes}
		if err := runtime.NewDocker(runtime.DockerSock()).BackupStorage(ctx, selected[0], writer); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := os.Link(file.Name(), *output); err != nil {
			return fmt.Errorf("backup destination must be new: %w", err)
		}
		fmt.Println(*output)
		return nil
	case "restore":
		if *input == "" || *directory == "" {
			return fmt.Errorf("restore requires -f and --directory")
		}
		file, err := os.Open(*input)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := volume.Restore(file, *directory, *maxBytes); err != nil {
			return err
		}
		fmt.Println(*directory)
		return nil
	default:
		return fmt.Errorf("unknown storage operation %q", args[0])
	}
}

type boundedArchive struct {
	file      *os.File
	remaining int64
}

func (w *boundedArchive) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, fmt.Errorf("backup exceeds --max-bytes")
	}
	n, err := w.file.Write(data)
	w.remaining -= int64(n)
	return n, err
}
