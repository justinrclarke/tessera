package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func (d *Docker) LockIdentity(ctx context.Context) (string, error) {
	body, err := d.call(ctx, http.MethodGet, "/v1.41/info", nil)
	if err != nil {
		return "", err
	}
	var info struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", err
	}
	if info.ID == "" {
		return "", fmt.Errorf("Docker did not report its daemon identity")
	}
	return "docker:" + info.ID, nil
}
