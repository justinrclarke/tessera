package controller

import (
	"encoding/json"
	"fmt"

	"tessera/internal/infra"
)

func infraManifest(desired infra.Desired) ([]byte, error) {
	body, err := json.Marshal(desired)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if apps, ok := raw["apps"].([]any); ok {
		for i, value := range apps {
			app := value.(map[string]any)
			delete(app, "release_env")
			delete(app, "env_resolved")
			app["resources"].(map[string]any)["cpu"] = fmt.Sprintf("%dm", desired.Apps[i].Resources.CPU)
		}
	}
	return json.Marshal(raw)
}
