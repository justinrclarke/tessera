package agent

import (
	"encoding/json"
	"os"
	"path/filepath"

	"tessera/internal/api"
	"tessera/internal/fileutil"
)

func (a *Agent) checkpointMarker(assignment api.Assignment) *api.CheckpointMarker {
	request := assignment.Checkpoint
	if request == nil || assignment.Status != api.StatusRunning || !a.now().Before(request.Deadline) {
		return nil
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil
	}
	path := filepath.Join(request.Directory, "requests", assignment.ID+".json")
	if err := fileutil.Write(path, body, 0o600); err != nil {
		return nil
	}
	markerPath := filepath.Join(request.Directory, "committed", request.ID, assignment.ID+".json")
	info, err := os.Stat(markerPath)
	if err != nil || info.Size() > 4096 {
		return nil
	}
	body, err = os.ReadFile(markerPath)
	if err != nil {
		return nil
	}
	var marker api.CheckpointMarker
	if json.Unmarshal(body, &marker) != nil || marker.RequestID != request.ID || marker.AssignmentID != assignment.ID || marker.URI != request.URI || marker.Progress < 0 {
		return nil
	}
	return &marker
}

func (a *Agent) checkpoint(assignment api.Assignment) string {
	marker := a.checkpointMarker(assignment)
	if marker == nil {
		return ""
	}
	return marker.RequestID
}
