package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"tessera/internal/cloud"
)

type CloudCLI struct {
	Provider string
	Settings cloud.Settings
	Run      cloud.Run
}

func (c CloudCLI) run(ctx context.Context, region string, out any, args ...string) error {
	name := ""
	switch c.Provider {
	case "aws":
		name = "aws"
		args = append(args, "--output", "json")
		if region != "" {
			args = append(args, "--region", region)
		}
	case "gcp":
		name = "gcloud"
		args = append(args, "--format=json", "--quiet")
		if c.Settings.Project != "" {
			args = append(args, "--project", c.Settings.Project)
		}
	case "azure":
		name = "az"
		args = append(args, "--output", "json", "--only-show-errors")
		if c.Settings.Subscription != "" {
			args = append(args, "--subscription", c.Settings.Subscription)
		}
	default:
		return fmt.Errorf("unknown provider %q", c.Provider)
	}
	run := c.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	body, err := run(ctx, name, args...)
	if err != nil {
		return fmt.Errorf("%s %s failed: %w", name, strings.Join(args[:min(2, len(args))], " "), err)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c CloudCLI) Discover(ctx context.Context) ([]cloud.Instance, error) {
	return (cloud.Inventory{Provider: c.Provider, Region: c.Settings.Region, Project: c.Settings.Project, Subscription: c.Settings.Subscription, Run: c.Run}).List(ctx)
}

func (c CloudCLI) find(ctx context.Context, spec cloud.MachineSpec) (*cloud.Instance, error) {
	var raw json.RawMessage
	switch c.Provider {
	case "aws":
		if err := c.run(ctx, spec.Region, &raw, "ec2", "describe-instances", "--filters", "Name=tag:tessera-key,Values="+spec.Key); err != nil {
			return nil, err
		}
	case "gcp":
		if err := c.run(ctx, "", &raw, "compute", "instances", "list", "--filter=labels.tessera-key="+spec.Key); err != nil {
			return nil, err
		}
	case "azure":
		if err := c.run(ctx, "", &raw, "vm", "list", "--show-details", "--resource-group", c.Settings.ResourceGroup); err != nil {
			return nil, err
		}
	}
	var items []cloud.Instance
	var err error
	switch c.Provider {
	case "aws":
		items, err = (cloud.Inventory{Provider: "aws", Run: func(context.Context, string, ...string) ([]byte, error) { return raw, nil }}).List(ctx)
	case "gcp":
		items, err = (cloud.Inventory{Provider: "gcp", Run: func(context.Context, string, ...string) ([]byte, error) { return raw, nil }}).List(ctx)
	case "azure":
		items, err = (cloud.Inventory{Provider: "azure", Run: func(context.Context, string, ...string) ([]byte, error) { return raw, nil }}).List(ctx)
	}
	if err != nil {
		return nil, err
	}
	var match *cloud.Instance
	for _, item := range items {
		if item.Name == spec.Name && item.Key != spec.Key {
			return nil, fmt.Errorf("instance name belongs to another owner")
		}
		if item.Key == spec.Key {
			if match != nil {
				return nil, fmt.Errorf("multiple instances have ownership key %s", spec.Key)
			}
			copy := item
			match = &copy
		}
	}
	return match, nil
}

func privateCloudFile(body []byte) (string, func(), error) {
	f, err := os.CreateTemp("", "tessera-cloud-*")
	if err != nil {
		return "", func() {}, err
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		os.Remove(f.Name())
		return "", func() {}, err
	}
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

func (c CloudCLI) Create(ctx context.Context, spec cloud.MachineSpec) (cloud.Resource, error) {
	if item, err := c.find(ctx, spec); err != nil {
		return cloud.Resource{}, err
	} else if item != nil {
		return cloud.Resource{ID: item.ID, Name: item.Name, Region: spec.Region, Key: spec.Key}, nil
	}
	resource := cloud.Resource{Name: spec.Name, Region: spec.Region, Key: spec.Key}
	file, cleanup, err := privateCloudFile([]byte(spec.UserData))
	if err != nil {
		return resource, err
	}
	defer cleanup()
	switch c.Provider {
	case "aws":
		input := map[string]any{"ImageId": spec.Image, "InstanceType": spec.Type, "MinCount": 1, "MaxCount": 1, "ClientToken": spec.Key, "UserData": base64.StdEncoding.EncodeToString([]byte(spec.UserData)), "TagSpecifications": []any{map[string]any{"ResourceType": "instance", "Tags": []any{map[string]string{"Key": "Name", "Value": spec.Name}, map[string]string{"Key": "tessera-key", "Value": spec.Key}}}}}
		if spec.Subnet != "" {
			input["SubnetId"] = spec.Subnet
		}
		if c.Settings.SSHKey != "" {
			input["KeyName"] = c.Settings.SSHKey
		}
		body, _ := json.Marshal(input)
		path, done, err := privateCloudFile(body)
		if err != nil {
			return resource, err
		}
		defer done()
		var result struct {
			Instances []struct {
				ID string `json:"InstanceId"`
			} `json:"Instances"`
		}
		if err := c.run(ctx, spec.Region, &result, "ec2", "run-instances", "--cli-input-json", "file://"+path); err != nil {
			return resource, err
		}
		if len(result.Instances) != 1 || result.Instances[0].ID == "" {
			return resource, fmt.Errorf("AWS did not return one instance")
		}
		resource.ID = result.Instances[0].ID
	case "gcp":
		args := []string{"compute", "instances", "create", spec.Name, "--zone", spec.Region, "--machine-type", spec.Type, "--image", spec.Image, "--no-address", "--labels=tessera-key=" + spec.Key, "--metadata-from-file=startup-script=" + file}
		if spec.Subnet != "" {
			args = append(args, "--subnet", spec.Subnet)
		} else if spec.Network != "" {
			args = append(args, "--network", spec.Network)
		}
		if err := c.run(ctx, "", nil, args...); err != nil {
			return resource, err
		}
	case "azure":
		if c.Settings.ResourceGroup == "" || c.Settings.AdminUser == "" || c.Settings.SSHKey == "" {
			return resource, fmt.Errorf("Azure requires resource_group, admin_user, and ssh_key")
		}
		args := []string{"vm", "create", "--name", spec.Name, "--resource-group", c.Settings.ResourceGroup, "--location", spec.Region, "--image", spec.Image, "--size", spec.Type, "--admin-username", c.Settings.AdminUser, "--ssh-key-values", c.Settings.SSHKey, "--custom-data", file, "--tags", "tessera-key=" + spec.Key, "--public-ip-address", "", "--nsg", ""}
		if spec.Network != "" {
			args = append(args, "--vnet-name", spec.Network, "--subnet", spec.Subnet)
		}
		if err := c.run(ctx, "", nil, args...); err != nil {
			return resource, err
		}
	}
	if resource.ID == "" {
		item, err := c.find(ctx, spec)
		if err != nil {
			return resource, err
		}
		if item == nil {
			return resource, fmt.Errorf("created instance not visible yet; retry reconciles by ownership key")
		}
		resource.ID = item.ID
	}
	return resource, nil
}

func (c CloudCLI) Inspect(ctx context.Context, r cloud.Resource) (cloud.Instance, error) {
	c.Settings.Region = r.Region
	items, err := c.Discover(ctx)
	if err != nil {
		return cloud.Instance{}, err
	}
	for _, item := range items {
		if item.ID == r.ID {
			if r.Key != "" && item.Key != r.Key {
				return cloud.Instance{}, fmt.Errorf("instance %s ownership changed", r.ID)
			}
			return item, nil
		}
	}
	return cloud.Instance{}, cloud.ErrNotFound
}

func (c CloudCLI) Stop(ctx context.Context, r cloud.Resource) error {
	if r.Key == "" {
		return fmt.Errorf("refusing removal without an ownership key")
	}
	if _, err := c.Inspect(ctx, r); err != nil {
		if errors.Is(err, cloud.ErrNotFound) {
			return nil
		}
		return err
	}
	switch c.Provider {
	case "aws":
		return c.run(ctx, r.Region, nil, "ec2", "terminate-instances", "--instance-ids", r.ID)
	case "gcp":
		return c.run(ctx, "", nil, "compute", "instances", "delete", r.Name, "--zone", r.Region)
	case "azure":
		return c.run(ctx, "", nil, "vm", "delete", "--ids", r.ID, "--yes")
	}
	return fmt.Errorf("unknown provider")
}
