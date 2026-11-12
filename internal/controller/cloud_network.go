package controller

import (
	"context"
	"fmt"
	"strings"

	"tessera/internal/cloud"
)

func cloudRegion(zone string) string {
	parts := strings.Split(zone, "-")
	if len(parts) > 2 && len(parts[len(parts)-1]) == 1 {
		return strings.Join(parts[:len(parts)-1], "-")
	}
	return zone
}

func (c CloudCLI) network(ctx context.Context, spec cloud.NetworkSpec) (*cloud.Resource, error) {
	var matches []cloud.Resource
	switch c.Provider {
	case "aws":
		var out struct {
			VPCs []struct {
				ID   string                        `json:"VpcId"`
				Tags []struct{ Key, Value string } `json:"Tags"`
			} `json:"Vpcs"`
		}
		if err := c.run(ctx, spec.Region, &out, "ec2", "describe-vpcs", "--filters", "Name=tag:tessera-key,Values="+spec.Key); err != nil {
			return nil, err
		}
		for _, v := range out.VPCs {
			for _, tag := range v.Tags {
				if tag.Key == "tessera-key" && tag.Value == spec.Key {
					matches = append(matches, cloud.Resource{ID: v.ID, Name: spec.Name, Region: spec.Region, Key: spec.Key})
				}
			}
		}
	case "gcp":
		var out []struct{ Name, Description string }
		if err := c.run(ctx, "", &out, "compute", "networks", "list"); err != nil {
			return nil, err
		}
		for _, v := range out {
			if v.Name == spec.Name && v.Description != "tessera:"+spec.Key {
				return nil, fmt.Errorf("network name belongs to another owner")
			}
			if v.Description == "tessera:"+spec.Key {
				matches = append(matches, cloud.Resource{ID: v.Name, Name: v.Name, Region: spec.Region, Key: spec.Key})
			}
		}
	case "azure":
		if c.Settings.ResourceGroup == "" {
			return nil, fmt.Errorf("Azure requires resource_group")
		}
		var out []struct {
			ID, Name string
			Tags     map[string]string
		}
		if err := c.run(ctx, "", &out, "network", "vnet", "list", "--resource-group", c.Settings.ResourceGroup); err != nil {
			return nil, err
		}
		for _, v := range out {
			if v.Name == spec.Name && v.Tags["tessera-key"] != spec.Key {
				return nil, fmt.Errorf("network name belongs to another owner")
			}
			if v.Tags["tessera-key"] == spec.Key {
				matches = append(matches, cloud.Resource{ID: v.ID, Name: v.Name, Region: spec.Region, Key: spec.Key})
			}
		}
	default:
		return nil, fmt.Errorf("unknown provider %q", c.Provider)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("multiple networks have ownership key %s", spec.Key)
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &matches[0], nil
}

func (c CloudCLI) CreateNetwork(ctx context.Context, spec cloud.NetworkSpec) (cloud.Resource, error) {
	if c.Provider == "aws" {
		return c.createAWSNetwork(ctx, spec)
	}
	r, err := c.network(ctx, spec)
	if err != nil {
		return cloud.Resource{}, err
	}
	if r == nil {
		switch c.Provider {
		case "gcp":
			if err := c.run(ctx, "", nil, "compute", "networks", "create", spec.Name, "--subnet-mode=custom", "--description=tessera:"+spec.Key); err != nil {
				return cloud.Resource{}, err
			}
		case "azure":
			if err := c.run(ctx, "", nil, "network", "vnet", "create", "--name", spec.Name, "--resource-group", c.Settings.ResourceGroup, "--location", spec.Region, "--address-prefixes", spec.CIDR, "--subnet-name", spec.Name, "--subnet-prefixes", spec.CIDR, "--tags", "tessera-key="+spec.Key); err != nil {
				return cloud.Resource{}, err
			}
		}
		r, err = c.network(ctx, spec)
		if err != nil {
			return cloud.Resource{}, err
		}
		if r == nil {
			return cloud.Resource{}, fmt.Errorf("created network not visible yet; retry by ownership key")
		}
	}
	switch c.Provider {
	case "gcp":
		var subnets []struct{ Name, Description, Region string }
		if err := c.run(ctx, "", &subnets, "compute", "networks", "subnets", "list", "--network", r.Name); err != nil {
			return *r, err
		}
		for _, subnet := range subnets {
			if subnet.Name == spec.Name && subnetRegion(subnet.Region, cloudRegion(spec.Region)) {
				if subnet.Description != "tessera:"+spec.Key {
					return *r, fmt.Errorf("subnet belongs to another owner")
				}
				r.Subnet = subnet.Name
			}
		}
		if r.Subnet == "" {
			if err := c.run(ctx, "", nil, "compute", "networks", "subnets", "create", spec.Name, "--network", r.Name, "--region", cloudRegion(spec.Region), "--range", spec.CIDR, "--description=tessera:"+spec.Key); err != nil {
				return *r, err
			}
			r.Subnet = spec.Name
		}
	case "azure":
		var subnets []struct{ Name string }
		if err := c.run(ctx, "", &subnets, "network", "vnet", "subnet", "list", "--resource-group", c.Settings.ResourceGroup, "--vnet-name", r.Name); err != nil {
			return *r, err
		}
		for _, subnet := range subnets {
			if subnet.Name == spec.Name {
				r.Subnet = subnet.Name
			}
		}
		if r.Subnet == "" {
			if err := c.run(ctx, "", nil, "network", "vnet", "subnet", "create", "--resource-group", c.Settings.ResourceGroup, "--vnet-name", r.Name, "--name", spec.Name, "--address-prefixes", spec.CIDR); err != nil {
				return *r, err
			}
			r.Subnet = spec.Name
		}
	}
	if r.Subnet == "" {
		return *r, fmt.Errorf("provider did not return a subnet")
	}
	return *r, nil
}

func (c CloudCLI) DeleteNetwork(ctx context.Context, r cloud.Resource) error {
	if r.Key == "" {
		return fmt.Errorf("refusing network removal without ownership")
	}
	if c.Provider == "aws" {
		return c.deleteAWSNetwork(ctx, r)
	}
	actual, err := c.network(ctx, cloud.NetworkSpec{Name: r.Name, Region: r.Region, Key: r.Key})
	if err != nil {
		return err
	}
	if actual == nil {
		return nil
	}
	if actual.ID != r.ID {
		return fmt.Errorf("network ownership changed")
	}
	switch c.Provider {
	case "gcp":
		var subnets []struct{ Name, Description, Region string }
		if err := c.run(ctx, "", &subnets, "compute", "networks", "subnets", "list", "--network", r.Name); err != nil {
			return err
		}
		for _, subnet := range subnets {
			if subnet.Name != r.Subnet || !subnetRegion(subnet.Region, cloudRegion(r.Region)) {
				continue
			}
			if subnet.Description != "tessera:"+r.Key {
				return fmt.Errorf("subnet belongs to another owner")
			}
			if err := c.run(ctx, "", nil, "compute", "networks", "subnets", "delete", r.Subnet, "--region", cloudRegion(r.Region)); err != nil {
				return err
			}
		}
		return c.run(ctx, "", nil, "compute", "networks", "delete", r.Name)
	case "azure":
		return c.run(ctx, "", nil, "network", "vnet", "delete", "--ids", r.ID)
	}
	return fmt.Errorf("unknown provider")
}

func (c CloudCLI) InspectNetwork(ctx context.Context, r cloud.Resource) error {
	actual, err := c.network(ctx, cloud.NetworkSpec{Name: r.Name, Region: r.Region, Key: r.Key})
	if err != nil {
		return err
	}
	if actual == nil {
		return cloud.ErrNotFound
	}
	if actual.ID != r.ID {
		return fmt.Errorf("network ownership changed")
	}
	return nil
}

func subnetRegion(value, expected string) bool {
	if value == "" {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(value, "/"), "/")
	return parts[len(parts)-1] == expected
}
