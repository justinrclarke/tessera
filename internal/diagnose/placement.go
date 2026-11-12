package diagnose

import (
	"fmt"
	"strings"

	"tessera/internal/schedule"
)

func Placement(plan schedule.Result) []Finding {
	var findings []Finding
	for _, blocked := range plan.Blocked {
		replicas := "replicas"
		if blocked.Missing == 1 {
			replicas = "replica"
		}
		state := fmt.Sprintf("%s has %d unplaced %s", blocked.App, blocked.Missing, replicas)
		if blocked.Replacement {
			state = blocked.App + " update is waiting"
		}
		reasons := []string{blocked.Reason}
		for _, node := range blocked.Nodes {
			reasons = append(reasons, node.NodeID+": "+node.Reason)
		}
		reason := strings.Join(reasons, "; ")
		findings = append(findings, Finding{Class: "placement", Target: blocked.App, Summary: state + ": " + reason, Reason: reason})
	}
	return findings
}
