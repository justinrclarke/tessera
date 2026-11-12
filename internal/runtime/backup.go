package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"tessera/internal/api"
)

func (d *Docker) BackupStorage(ctx context.Context, assignment api.Assignment, out io.Writer) (result error) {
	if assignment.Storage == nil || assignment.RuntimeID == "" {
		return fmt.Errorf("backup requires a running persistent assignment")
	}
	base := "/v1.41/containers/" + url.PathEscape(assignment.RuntimeID)
	body, err := d.call(ctx, http.MethodGet, base+"/json", nil)
	if err != nil {
		return err
	}
	var inspected struct {
		Name   string
		State  struct{ Running, Paused bool }
		Config struct{ Labels map[string]string }
		Mounts []struct{ Type, Source, Destination string }
	}
	if err := json.Unmarshal(body, &inspected); err != nil {
		return err
	}
	if inspected.Name != "/tessera_"+assignment.ID || inspected.Config.Labels["tessera.node"] != assignment.NodeID || !inspected.State.Running {
		return fmt.Errorf("local Docker container does not match the running assignment and its owner")
	}
	matched := false
	for _, mount := range inspected.Mounts {
		matched = matched || mount.Type == "bind" && mount.Source == assignment.Storage.Source && mount.Destination == assignment.Storage.Target
	}
	if !matched {
		return fmt.Errorf("Docker storage mount does not match the assignment")
	}
	if !inspected.State.Paused {
		if _, err := d.call(ctx, http.MethodPost, base+"/pause", nil); err != nil {
			return err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := d.call(cleanup, http.MethodPost, base+"/unpause", nil); err != nil {
				result = errors.Join(result, fmt.Errorf("could not resume container %s after backup: %w", assignment.RuntimeID, err))
			}
		}()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+base+"/archive?path="+url.QueryEscape(assignment.Storage.Target), nil)
	if err != nil {
		return err
	}
	response, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Docker archive returned HTTP %d", response.StatusCode)
	}
	_, err = io.Copy(out, response.Body)
	return err
}
