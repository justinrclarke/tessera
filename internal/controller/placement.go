package controller

import (
	"fmt"

	"tessera/internal/api"
	"tessera/internal/diagnose"
	"tessera/internal/schedule"
)

func (s *Server) placementFindings(apps []api.App, nodes []api.Node, assignments []api.Assignment, pol api.Policy) ([]diagnose.Finding, error) {
	configs, err := s.Store.ListConfigs()
	if err != nil {
		return nil, err
	}
	secrets, err := s.Store.ListSecrets()
	if err != nil {
		return nil, err
	}
	knownConfigs, knownSecrets := map[string]bool{}, map[string]bool{}
	for _, config := range configs {
		knownConfigs[config.Name] = true
	}
	for _, secret := range secrets {
		knownSecrets[secret.Name] = true
	}
	var findings []diagnose.Finding
	missing := map[string]bool{}
	for _, app := range apps {
		if app.EnvResolved || app.Replicas == 0 {
			continue
		}
		for _, ref := range []struct {
			kind  string
			names []string
			known map[string]bool
		}{{"Config", app.Configs, knownConfigs}, {"Secret", app.Secrets, knownSecrets}} {
			for _, name := range ref.names {
				if !ref.known[name] {
					reason := fmt.Sprintf("missing %s/%s", ref.kind, name)
					findings = append(findings, diagnose.Finding{Class: "configuration", Target: app.Name, Summary: app.Name + " cannot resolve its environment: " + reason, Reason: reason})
					missing[app.Name] = true
				}
			}
		}
	}
	plan := schedule.Plan(schedule.Input{Apps: apps, Nodes: nodes, Assignments: assignments, MaxRestarts: pol.MaxRestarts})
	for _, finding := range diagnose.Placement(plan) {
		if !missing[finding.Target] {
			findings = append(findings, finding)
		}
	}
	return findings, nil
}
