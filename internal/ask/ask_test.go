package ask

import (
	"strings"
	"testing"

	"tessera/internal/api"
	"tessera/internal/diagnose"
)

func TestExplainFocusesOnTheNamedApp(t *testing.T) {
	apps := []api.App{{Name: "api"}, {Name: "web"}, {Name: "web-api"}}
	for _, test := range []struct {
		question string
		focus    string
	}{
		{"why is web-api down?", "web-api"},
		{"why is WEB down?", "web"},
		{"what capacity is available?", ""},
		{"how are the webworkers doing?", ""},
		{"is web running?", "web"},
		{"web. Is it running?", "web"},
	} {
		if got := matchApp(test.question, apps); got != test.focus {
			t.Fatalf("%q focused %q, want %q", test.question, got, test.focus)
		}
	}
	text := Explain(View{
		Question: "why is web down?", Apps: apps,
		Findings: []diagnose.Finding{{Target: "web", Summary: "web needs more CPU"}, {Target: "web-api", Summary: "web-api needs a GPU"}},
		Actions:  []api.Action{{Target: "web-api", Kind: "move", Reason: "faster than web"}},
	})
	if !strings.Contains(text, "web needs more CPU") || strings.Contains(text, "web-api") || strings.Contains(text, "Actions:") {
		t.Fatalf("focused answer includes another app: %s", text)
	}
}
