package diagnose

import (
	"fmt"
	"strings"
	"time"

	"tessera/internal/api"
)

type Finding struct {
	Class   string
	Target  string
	Action  string
	Summary string
	Reason  string
}

func Scan(apps []api.App, nodes []api.Node, asgs []api.Assignment, maxRestarts int, now time.Time) []Finding {
	if maxRestarts == 0 {
		maxRestarts = 3
	}
	byNode := map[string]api.Node{}
	for _, n := range nodes {
		byNode[n.ID] = n
	}
	byApp := map[string]api.App{}
	for _, a := range apps {
		byApp[a.Name] = a
	}
	running := map[string]int{}
	for _, asg := range asgs {
		if asg.Status == api.StatusRunning {
			running[asg.App]++
		}
	}
	var out []Finding
	for _, n := range nodes {
		if n.Status == api.NodeDead {
			out = append(out, Finding{
				Class: "node_dead", Target: n.ID, Action: "reschedule",
				Summary: fmt.Sprintf("node %s missed heartbeats and is dead; work on it will move", n.ID),
				Reason:  "heartbeat timeout",
			})
		}
		if n.DiskTotal > 0 && n.DiskFree*10 < n.DiskTotal {
			out = append(out, Finding{
				Class: "disk_full", Target: n.ID, Action: "prune",
				Summary: fmt.Sprintf("disk on %s is over 90 percent full", n.ID),
				Reason:  "disk pressure",
			})
		}
		if needsRenew(n, now) {
			out = append(out, Finding{
				Class: "cert_expiry", Target: n.ID, Action: "renew",
				Summary: fmt.Sprintf("certificate for %s is past half its life", n.ID),
				Reason:  "cert half-life",
			})
		}
	}
	fails := map[string]int{}
	for _, asg := range asgs {
		if asg.Status != api.StatusFailed {
			continue
		}
		fails[asg.NodeID]++
		app := byApp[asg.App]
		class, action := classify(asg, app, maxRestarts)
		out = append(out, Finding{
			Class: class, Target: asg.App, Action: action,
			Summary: fmt.Sprintf("%s on %s is %s (%s)", asg.App, asg.NodeID, class, asg.Reason),
			Reason:  asg.Reason,
		})
	}
	for id, n := range fails {
		if n >= maxRestarts && byNode[id].Status != api.NodeCordoned && byNode[id].Status != api.NodeDead {
			out = append(out, Finding{
				Class: "node_fault", Target: id, Action: "cordon",
				Summary: fmt.Sprintf("node %s failed %d workloads; cordoning it", id, n),
				Reason:  "repeated failure",
			})
		}
	}
	for _, app := range apps {
		for _, dep := range app.DependsOn {
			if running[dep] == 0 {
				out = append(out, Finding{
					Class: "dependency", Target: app.Name, Action: "hold",
					Summary: fmt.Sprintf("%s is held until %s is running", app.Name, dep),
					Reason:  "dependency down",
				})
			}
		}
	}
	return out
}

func classify(asg api.Assignment, app api.App, maxRestarts int) (string, string) {
	if app.HealthyGeneration > 0 && app.Generation > app.HealthyGeneration {
		return "bad_release", "rollback"
	}
	reason := strings.ToLower(asg.Reason)
	switch {
	case strings.Contains(reason, "oom") || strings.Contains(reason, "137"):
		return "oom", "reschedule"
	case strings.Contains(reason, "pull"):
		return "pull_fail", "restart"
	case strings.Contains(reason, "health"):
		if asg.Restarts >= maxRestarts {
			return "health_fail", "reschedule"
		}
		return "health_fail", "restart"
	case strings.Contains(reason, "depend"):
		return "dependency", "hold"
	default:
		if asg.Restarts >= maxRestarts {
			return "crash_loop", "reschedule"
		}
		return "crash_loop", "restart"
	}
}

func needsRenew(n api.Node, now time.Time) bool {
	if n.CertNotAfter.IsZero() || n.CertNotBefore.IsZero() {
		return false
	}
	if !now.Before(n.CertNotAfter) {
		return true
	}
	life := n.CertNotAfter.Sub(n.CertNotBefore)
	return n.CertNotAfter.Sub(now) < life/2
}
