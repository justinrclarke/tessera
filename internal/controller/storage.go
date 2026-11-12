package controller

import (
	"fmt"

	"tessera/internal/api"
)

func (s *Server) putApp(app api.App) error {
	previous, err := s.Store.GetApp(app.Name)
	if err == nil && (previous.Storage != nil || app.Storage != nil) && !previous.ReleaseEqual(app) {
		assignments, err := s.Store.ListAssignments()
		if err != nil {
			return err
		}
		for _, assignment := range assignments {
			if assignment.App == app.Name && api.Active(assignment.Status) {
				return fmt.Errorf("app %s uses persistent storage: scale to zero and wait for stopped assignments before changing its release", app.Name)
			}
		}
	}
	return s.Store.PutApp(app)
}

func (s *Server) validateStorage() error {
	apps, err := s.Store.ListApps()
	if err != nil {
		return err
	}
	assignments, err := s.Store.ListAssignments()
	if err != nil {
		return err
	}
	for _, app := range apps {
		for _, other := range apps {
			if app.Name != other.Name && api.StorageOverlaps(app.Storage, other.Storage) {
				return fmt.Errorf("apps %s and %s claim overlapping storage on the same node", app.Name, other.Name)
			}
		}
		for _, assignment := range assignments {
			if !api.Active(assignment.Status) {
				continue
			}
			if assignment.App == app.Name && assignment.Storage != nil && (app.Storage == nil || *assignment.Storage != *app.Storage) {
				return fmt.Errorf("app %s still has a storage assignment awaiting stop acknowledgement", app.Name)
			}
			if assignment.App != app.Name && api.StorageOverlaps(app.Storage, assignment.Storage) {
				return fmt.Errorf("storage is still held by assignment %s; wait for its stop acknowledgement", assignment.ID)
			}
		}
	}
	return nil
}
