package api

import "time"

type Checkpoint struct {
	Directory string `json:"directory" yaml:"directory"`
	Timeout   string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

type CheckpointRequest struct {
	ID        string    `json:"id"`
	Directory string    `json:"directory"`
	URI       string    `json:"uri"`
	Deadline  time.Time `json:"deadline"`
}

type CheckpointMarker struct {
	RequestID    string `json:"request_id"`
	AssignmentID string `json:"assignment_id"`
	URI          string `json:"uri"`
	Progress     int64  `json:"progress"`
}
