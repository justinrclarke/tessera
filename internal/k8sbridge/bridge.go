package k8sbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"tessera/internal/diagnose"
)

type Bridge struct {
	Kubeconfig string
	Context    string
	Namespace  string
	Bin        string
	Run        func(context.Context, []string) ([]byte, error)
}

type Workload struct {
	Kind       string   `json:"kind"`
	Namespace  string   `json:"namespace"`
	Name       string   `json:"name"`
	Image      string   `json:"image"`
	Desired    int      `json:"desired"`
	Ready      int      `json:"ready"`
	Pods       []Pod    `json:"pods,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	selector   selector
}

type Pod struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Node      string            `json:"node,omitempty"`
	Phase     string            `json:"phase"`
	Reason    string            `json:"reason,omitempty"`
	Restarts  int               `json:"restarts"`
	Labels    map[string]string `json:"-"`
	Warnings  []string          `json:"warnings,omitempty"`
}

type Node struct {
	Name       string   `json:"name"`
	Ready      bool     `json:"ready"`
	CPU        string   `json:"cpu,omitempty"`
	Memory     string   `json:"memory,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
}

type Snapshot struct {
	Workloads []Workload `json:"workloads"`
	Nodes     []Node     `json:"nodes"`
}

type selector struct {
	MatchLabels      map[string]string
	MatchExpressions []struct {
		Key      string   `json:"key"`
		Operator string   `json:"operator"`
		Values   []string `json:"values"`
	} `json:"matchExpressions"`
}

type metadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
	UID       string            `json:"uid"`
}

type container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type item struct {
	Kind     string   `json:"kind"`
	Metadata metadata `json:"metadata"`
	Spec     struct {
		Replicas    *int     `json:"replicas"`
		Parallelism *int     `json:"parallelism"`
		Completions *int     `json:"completions"`
		NodeName    string   `json:"nodeName"`
		Selector    selector `json:"selector"`
		Template    struct {
			Spec struct {
				Containers []container `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		Phase                  string `json:"phase"`
		Reason                 string `json:"reason"`
		Message                string `json:"message"`
		ReadyReplicas          int    `json:"readyReplicas"`
		DesiredNumberScheduled int    `json:"desiredNumberScheduled"`
		NumberReady            int    `json:"numberReady"`
		Succeeded              int    `json:"succeeded"`
		Capacity               struct {
			CPU    string `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"capacity"`
		Conditions        []condition `json:"conditions"`
		ContainerStatuses []struct {
			RestartCount int `json:"restartCount"`
			State        struct {
				Waiting struct {
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"waiting"`
				Terminated struct {
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"terminated"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

type event struct {
	Metadata  metadata `json:"metadata"`
	Type      string   `json:"type"`
	Reason    string   `json:"reason"`
	Message   string   `json:"message"`
	Regarding struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		UID  string `json:"uid"`
	} `json:"regarding"`
	InvolvedObject struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		UID  string `json:"uid"`
	} `json:"involvedObject"`
}

func (b *Bridge) command(ctx context.Context, args ...string) ([]byte, error) {
	flags := []string{"--request-timeout=10s"}
	if b.Kubeconfig != "" {
		flags = append(flags, "--kubeconfig", b.Kubeconfig)
	}
	if b.Context != "" {
		flags = append(flags, "--context", b.Context)
	}
	flags = append(flags, args...)
	if b.Run != nil {
		return b.Run(ctx, flags)
	}
	bin := b.Bin
	if bin == "" {
		bin = "kubectl"
	}
	out, err := exec.CommandContext(ctx, bin, flags...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (b *Bridge) scope(args ...string) []string {
	if b.Namespace == "" {
		return append(args, "-A")
	}
	return append(args, "-n", b.Namespace)
}

func (b *Bridge) list(ctx context.Context, resource string, namespaced bool) ([]item, error) {
	args := []string{"get", resource}
	if namespaced {
		args = b.scope(args...)
	}
	args = append(args, "-o", "json")
	raw, err := b.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	var body struct {
		Items []item `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode kubectl %s: %w", resource, err)
	}
	return body.Items, nil
}

func (b *Bridge) events(ctx context.Context) ([]event, error) {
	args := b.scope("get", "events")
	args = append(args, "-o", "json")
	raw, err := b.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	var body struct {
		Items []event `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode kubectl events: %w", err)
	}
	return body.Items, nil
}

func (b *Bridge) Inspect(ctx context.Context) (Snapshot, error) {
	objects, err := b.list(ctx, "deployments,statefulsets,daemonsets,jobs,pods", true)
	if err != nil {
		return Snapshot{}, err
	}
	allNodes, err := b.list(ctx, "nodes", false)
	if err != nil {
		return Snapshot{}, err
	}
	events, err := b.events(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	var pods []Pod
	for _, obj := range objects {
		switch obj.Kind {
		case "Deployment", "StatefulSet", "DaemonSet", "Job":
			w := Workload{Kind: obj.Kind, Namespace: obj.Metadata.Namespace, Name: obj.Metadata.Name, selector: obj.Spec.Selector}
			if len(obj.Spec.Template.Spec.Containers) > 0 {
				w.Image = obj.Spec.Template.Spec.Containers[0].Image
			}
			switch obj.Kind {
			case "DaemonSet":
				w.Desired, w.Ready = obj.Status.DesiredNumberScheduled, obj.Status.NumberReady
			case "Job":
				w.Desired, w.Ready = 1, obj.Status.Succeeded
				if obj.Spec.Completions != nil {
					w.Desired = *obj.Spec.Completions
				}
			default:
				w.Desired, w.Ready = 1, obj.Status.ReadyReplicas
				if obj.Spec.Replicas != nil {
					w.Desired = *obj.Spec.Replicas
				}
			}
			for _, c := range obj.Status.Conditions {
				if c.Status == "False" {
					w.Conditions = append(w.Conditions, conditionText(c))
				}
			}
			snap.Workloads = append(snap.Workloads, w)
		case "Pod":
			p := Pod{Namespace: obj.Metadata.Namespace, Name: obj.Metadata.Name, Node: obj.Spec.NodeName, Phase: obj.Status.Phase, Labels: obj.Metadata.Labels}
			if obj.Status.Reason != "" {
				p.Reason = obj.Status.Reason
			} else if obj.Status.Message != "" {
				p.Reason = obj.Status.Message
			}
			for _, s := range obj.Status.ContainerStatuses {
				p.Restarts += s.RestartCount
				if s.State.Waiting.Reason != "" {
					p.Reason = strings.TrimSpace(s.State.Waiting.Reason + ": " + s.State.Waiting.Message)
				} else if s.State.Terminated.Reason != "" && p.Phase != "Succeeded" {
					p.Reason = strings.TrimSpace(s.State.Terminated.Reason + ": " + s.State.Terminated.Message)
				}
			}
			for _, c := range obj.Status.Conditions {
				if p.Reason == "" && c.Status == "False" && c.Reason != "" {
					p.Reason = conditionText(c)
				}
			}
			for _, e := range events {
				if e.Type == "Warning" && e.Metadata.Namespace == p.Namespace && eventTarget(e) == p.Name {
					p.Warnings = append(p.Warnings, eventText(e))
				}
			}
			pods = append(pods, p)
		}
	}
	for i := range snap.Workloads {
		w := &snap.Workloads[i]
		for _, p := range pods {
			if w.Namespace == p.Namespace && w.selector.matches(p.Labels) {
				w.Pods = append(w.Pods, p)
			}
		}
		for _, e := range events {
			if e.Type == "Warning" && e.Metadata.Namespace == w.Namespace && eventTarget(e) == w.Name {
				w.Warnings = append(w.Warnings, eventText(e))
			}
		}
	}
	for _, obj := range allNodes {
		if obj.Kind != "Node" {
			continue
		}
		n := Node{Name: obj.Metadata.Name, CPU: obj.Status.Capacity.CPU, Memory: obj.Status.Capacity.Memory}
		for _, c := range obj.Status.Conditions {
			if c.Type == "Ready" {
				n.Ready = c.Status == "True"
			}
			if c.Status == "False" || c.Status == "Unknown" {
				n.Conditions = append(n.Conditions, conditionText(c))
			}
		}
		snap.Nodes = append(snap.Nodes, n)
	}
	sort.Slice(snap.Workloads, func(i, j int) bool {
		return snap.Workloads[i].Namespace+"/"+snap.Workloads[i].Name < snap.Workloads[j].Namespace+"/"+snap.Workloads[j].Name
	})
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].Name < snap.Nodes[j].Name })
	return snap, nil
}

func (s selector) matches(labels map[string]string) bool {
	if len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0 {
		return false
	}
	for k, v := range s.MatchLabels {
		if labels[k] != v {
			return false
		}
	}
	for _, e := range s.MatchExpressions {
		value, present := labels[e.Key]
		contained := false
		for _, v := range e.Values {
			contained = contained || v == value
		}
		switch e.Operator {
		case "In":
			if !present || !contained {
				return false
			}
		case "NotIn":
			if present && contained {
				return false
			}
		case "Exists":
			if !present {
				return false
			}
		case "DoesNotExist":
			if present {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func conditionText(c condition) string {
	return strings.TrimSpace(strings.Join([]string{c.Type, c.Reason, c.Message}, ": "))
}

func eventTarget(e event) string {
	if e.Regarding.Name != "" {
		return e.Regarding.Name
	}
	return e.InvolvedObject.Name
}

func eventText(e event) string {
	return strings.TrimSpace(e.Reason + ": " + e.Message)
}

func (s Snapshot) Workload(name string) (Workload, error) {
	var found []Workload
	for _, w := range s.Workloads {
		if name == w.Namespace+"/"+w.Name || name == w.Name {
			found = append(found, w)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		return Workload{}, fmt.Errorf("workload %q is ambiguous; use namespace/name", name)
	}
	return Workload{}, fmt.Errorf("workload %q not found", name)
}

func (s Snapshot) Node(name string) (Node, error) {
	for _, n := range s.Nodes {
		if n.Name == name {
			return n, nil
		}
	}
	return Node{}, fmt.Errorf("node %q not found", name)
}

func (s Snapshot) Findings() []diagnose.Finding {
	var out []diagnose.Finding
	for _, n := range s.Nodes {
		if !n.Ready {
			out = append(out, diagnose.Finding{Class: "node_not_ready", Target: n.Name, Action: "inspect", Summary: "Kubernetes node " + n.Name + " is not Ready", Reason: strings.Join(n.Conditions, "; ")})
		}
	}
	for _, w := range s.Workloads {
		if w.Ready >= w.Desired {
			continue
		}
		reason := "no matching pods"
		if len(w.Conditions) > 0 {
			reason = w.Conditions[0]
		}
		for _, p := range w.Pods {
			if p.Reason != "" {
				reason = p.Name + ": " + p.Reason
				break
			}
			if len(p.Warnings) > 0 {
				reason = p.Name + ": " + p.Warnings[0]
				break
			}
		}
		if len(w.Warnings) > 0 && reason == "no matching pods" {
			reason = w.Warnings[0]
		}
		name := w.Namespace + "/" + w.Name
		out = append(out, diagnose.Finding{Class: "workload_unavailable", Target: name, Action: "inspect", Summary: fmt.Sprintf("%s %s is ready %d/%d: %s", w.Kind, name, w.Ready, w.Desired, reason), Reason: reason})
	}
	return out
}

func (s Snapshot) Explain(question string) string {
	var b strings.Builder
	var focus *Workload
	for i := range s.Workloads {
		w := &s.Workloads[i]
		if strings.Contains(strings.ToLower(question), strings.ToLower(w.Namespace+"/"+w.Name)) || strings.Contains(strings.ToLower(question), strings.ToLower(w.Name)) {
			focus = w
			break
		}
	}
	if focus != nil {
		fmt.Fprintf(&b, "%s %s/%s is ready %d/%d.\n", focus.Kind, focus.Namespace, focus.Name, focus.Ready, focus.Desired)
		for _, f := range s.Findings() {
			if f.Target == focus.Namespace+"/"+focus.Name {
				fmt.Fprintf(&b, "- %s\n", f.Summary)
			}
		}
		if focus.Ready >= focus.Desired {
			b.WriteString("No availability problem found for this workload.\n")
		}
		return b.String()
	}
	ready := 0
	for _, n := range s.Nodes {
		if n.Ready {
			ready++
		}
	}
	fmt.Fprintf(&b, "%d Kubernetes workloads, %d/%d nodes Ready.\n", len(s.Workloads), ready, len(s.Nodes))
	findings := s.Findings()
	if len(findings) == 0 {
		b.WriteString("No availability problems found.\n")
	} else {
		for _, f := range findings {
			fmt.Fprintf(&b, "- %s\n", f.Summary)
		}
	}
	return b.String()
}

func (b *Bridge) Logs(ctx context.Context, name string) (string, error) {
	snap, err := b.Inspect(ctx)
	if err != nil {
		return "", err
	}
	w, err := snap.Workload(name)
	if err != nil {
		return "", err
	}
	if len(w.Pods) == 0 {
		return "", fmt.Errorf("workload %s/%s has no matching pods", w.Namespace, w.Name)
	}
	args := []string{"logs", "-n", w.Namespace, "pod/" + w.Pods[0].Name, "--all-containers=true", "--tail=200"}
	raw, err := b.command(ctx, args...)
	return string(raw), err
}
