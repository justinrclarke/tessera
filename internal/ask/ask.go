package ask

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"tessera/internal/api"
	"tessera/internal/diagnose"
)

type View struct {
	Question    string
	Apps        []api.App
	Nodes       []api.Node
	Assignments []api.Assignment
	Actions     []api.Action
	Findings    []diagnose.Finding
}

func Explain(v View) string {
	var b strings.Builder
	focus := matchApp(v.Question, v.Apps)
	findings := v.Findings
	actions := v.Actions
	if focus != "" {
		findings = filterFindings(findings, focus)
		actions = filterActions(actions, focus)
		fmt.Fprintf(&b, "%s\n", appLine(findApp(v.Apps, focus), v.Assignments))
	} else {
		ready := 0
		for _, n := range v.Nodes {
			if n.Status == api.NodeReady {
				ready++
			}
		}
		running := 0
		for _, a := range v.Assignments {
			if a.Status == api.StatusRunning {
				running++
			}
		}
		fmt.Fprintf(&b, "%d apps, %d running assignments, %d/%d nodes ready.\n", len(v.Apps), running, ready, len(v.Nodes))
	}
	if len(findings) == 0 && len(actions) == 0 {
		b.WriteString("Nothing is failing. No recent actions.\n")
		return b.String()
	}
	if len(findings) > 0 {
		b.WriteString("Findings:\n")
		for _, f := range findings {
			fmt.Fprintf(&b, "- %s\n", f.Summary)
		}
	}
	if len(actions) > 0 {
		b.WriteString("Actions:\n")
		for _, a := range actions {
			fmt.Fprintf(&b, "- %s %s on %s (%s) -> %s\n", a.At.Format(time.RFC3339), a.Kind, a.Target, a.Reason, a.Result)
		}
	}
	return b.String()
}

type Completer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

func WithModel(ctx context.Context, base string, c Completer) string {
	if c == nil {
		return base
	}
	note, err := c.Complete(ctx, base)
	if err != nil || strings.TrimSpace(note) == "" {
		return base
	}
	return base + "\nNote: " + strings.TrimSpace(note) + "\n"
}

type OpenAI struct {
	URL   string
	Key   string
	Model string
	HTTP  *http.Client
}

func (o OpenAI) Complete(ctx context.Context, prompt string) (string, error) {
	if o.URL == "" || o.Key == "" {
		return "", nil
	}
	model := o.Model
	if model == "" {
		model = "gpt-4.1"
	}
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": "You explain a cluster diagnosis. Do not invent actions that are not in the text. Two sentences."},
			{"role": "user", "content": prompt},
		},
	})
	endpoint := strings.TrimRight(o.URL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+o.Key)
	req.Header.Set("Content-Type", "application/json")
	client := o.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("llm %s", resp.Status)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", nil
	}
	return parsed.Choices[0].Message.Content, nil
}

func matchApp(q string, apps []api.App) string {
	ql := strings.ToLower(q)
	for _, a := range apps {
		if a.Name != "" && strings.Contains(ql, strings.ToLower(a.Name)) {
			return a.Name
		}
	}
	return ""
}

func findApp(apps []api.App, name string) api.App {
	for _, a := range apps {
		if a.Name == name {
			return a
		}
	}
	return api.App{Name: name}
}

func appLine(a api.App, asgs []api.Assignment) string {
	ready := 0
	var nodes []string
	for _, asg := range asgs {
		if asg.App != a.Name || !api.Active(asg.Status) {
			continue
		}
		if asg.Status == api.StatusRunning {
			ready++
		}
		nodes = append(nodes, asg.NodeID+":"+asg.Status)
	}
	return fmt.Sprintf("%s image=%s generation=%d healthy=%d ready=%d/%d [%s]", a.Name, a.Image, a.Generation, a.HealthyGeneration, ready, a.Replicas, strings.Join(nodes, ", "))
}

func filterFindings(in []diagnose.Finding, name string) []diagnose.Finding {
	var out []diagnose.Finding
	for _, f := range in {
		if f.Target == name || strings.Contains(f.Summary, name) {
			out = append(out, f)
		}
	}
	return out
}

func filterActions(in []api.Action, name string) []api.Action {
	var out []api.Action
	for _, a := range in {
		if a.Target == name || strings.Contains(a.Reason, name) {
			out = append(out, a)
		}
	}
	return out
}
