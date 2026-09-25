package main

import "testing"

func TestParseLabels(t *testing.T) {
	labels, err := parseLabels("fabric=ethernet-a,storage=shared")
	if err != nil || labels["fabric"] != "ethernet-a" || labels["storage"] != "shared" {
		t.Fatalf("labels %+v: %v", labels, err)
	}
	for _, raw := range []string{"fabric", "=east", "fabric=", "fabric=east,fabric=west"} {
		if _, err := parseLabels(raw); err == nil {
			t.Fatalf("accepted invalid labels %q", raw)
		}
	}
}
