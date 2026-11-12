package store

import (
	"encoding/json"
	"sort"

	"tessera/internal/api"
)

func (s *Store) PruneHistory() error {
	if _, err := s.exec(`DELETE FROM actions WHERE id IN (SELECT id FROM actions WHERE json_extract(body,'$.result') NOT IN ('proposed','confirmed') ORDER BY at DESC,id DESC LIMIT -1 OFFSET 1000)`); err != nil {
		return err
	}
	apps, err := s.ListApps()
	if err != nil {
		return err
	}
	current := map[string]api.App{}
	protected := map[string]bool{}
	for _, app := range apps {
		current[app.Name] = app
		if _, err := s.exec(`DELETE FROM app_history WHERE name=? AND generation!=? AND generation!=? AND generation NOT IN (SELECT generation FROM app_history WHERE name=? ORDER BY generation DESC LIMIT 20)`, app.Name, app.Generation, app.HealthyGeneration, app.Name); err != nil {
			return err
		}
		raw, err := s.Meta("checkpoint:" + app.Name)
		if err != nil {
			return err
		}
		if raw != "" {
			var move struct {
				Phase string
				Old   []string
				New   []string
			}
			if err := json.Unmarshal([]byte(raw), &move); err != nil {
				return err
			}
			if move.Phase == "saving" || move.Phase == "stopping" || move.Phase == "starting" {
				for _, id := range append(move.Old, move.New...) {
					protected[id] = true
				}
			}
		}
	}
	assignments, err := s.ListAssignments()
	if err != nil {
		return err
	}
	sort.Slice(assignments, func(i, j int) bool {
		if assignments[i].Updated.Equal(assignments[j].Updated) {
			return assignments[i].ID > assignments[j].ID
		}
		return assignments[i].Updated.After(assignments[j].Updated)
	})
	retained := 0
	for _, a := range assignments {
		app, exists := current[a.App]
		if api.Active(a.Status) || protected[a.ID] || exists && app.Kind == api.KindJob && a.Generation == app.Generation && a.Status == api.StatusSucceeded {
			continue
		}
		retained++
		if retained > 1000 {
			if _, err := s.exec(`DELETE FROM assignments WHERE id=?`, a.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
