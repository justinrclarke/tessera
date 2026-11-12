package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"tessera/internal/api"
	"tessera/internal/fileutil"
)

func cmdCheckpoint(args []string) error {
	if len(args) == 0 || args[0] != "commit" {
		return fmt.Errorf("usage: tessera checkpoint commit --request FILE --assignment ID --progress N")
	}
	fs := flag.NewFlagSet("checkpoint commit", flag.ContinueOnError)
	requestFile := fs.String("request", "", "worker request JSON")
	assignment := fs.String("assignment", "", "worker assignment ID")
	progress := fs.Int64("progress", -1, "committed training step")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	safe := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if *requestFile == "" || !safe.MatchString(*assignment) || *progress < 0 || fs.NArg() != 0 {
		return fmt.Errorf("request, assignment, and nonnegative progress are required")
	}
	body, err := os.ReadFile(*requestFile)
	if err != nil {
		return err
	}
	var request api.CheckpointRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return err
	}
	if !safe.MatchString(request.ID) || !time.Now().Before(request.Deadline) {
		return fmt.Errorf("checkpoint request is invalid or expired")
	}
	uri, err := url.Parse(request.URI)
	if err != nil || uri.Scheme != "file" || uri.Host != "" || filepath.Clean(uri.Path) != filepath.Join(request.Directory, "generations", request.ID) {
		return fmt.Errorf("checkpoint URI does not match request directory")
	}
	workerDir := filepath.Join(uri.Path, *assignment)
	info, err := os.Stat(workerDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("save and sync checkpoint data under %s before committing", workerDir)
	}
	marker := api.CheckpointMarker{RequestID: request.ID, AssignmentID: *assignment, URI: request.URI, Progress: *progress}
	body, err = json.Marshal(marker)
	if err != nil {
		return err
	}
	return fileutil.Write(filepath.Join(request.Directory, "committed", request.ID, *assignment+".json"), body, 0600)
}
