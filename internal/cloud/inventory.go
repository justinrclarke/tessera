package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

type Instance struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Region   string `json:"region"`
	Type     string `json:"type"`
	State    string `json:"state"`
	Private  string `json:"private_address,omitempty"`
	Public   string `json:"public_address,omitempty"`
}

type Run func(context.Context, string, ...string) ([]byte, error)

type Inventory struct {
	Provider     string
	Region       string
	Project      string
	Subscription string
	Run          Run
}

func (i Inventory) List(ctx context.Context) ([]Instance, error) {
	run := i.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	var name string
	var args []string
	switch i.Provider {
	case "aws":
		name = "aws"
		args = []string{"ec2", "describe-instances", "--output", "json"}
		if i.Region != "" {
			args = append(args, "--region", i.Region)
		}
	case "gcp":
		name = "gcloud"
		args = []string{"compute", "instances", "list", "--format=json"}
		if i.Project != "" {
			args = append(args, "--project", i.Project)
		}
	case "azure":
		name = "az"
		args = []string{"vm", "list", "--show-details", "--output", "json"}
		if i.Subscription != "" {
			args = append(args, "--subscription", i.Subscription)
		}
	default:
		return nil, fmt.Errorf("unknown cloud provider %q; use aws, gcp, or azure", i.Provider)
	}
	output, err := run(ctx, name, args...)
	if err != nil {
		return nil, fmt.Errorf("%s inventory: %w: %s", i.Provider, err, strings.TrimSpace(string(output)))
	}
	var instances []Instance
	switch i.Provider {
	case "aws":
		instances, err = parseAWS(output)
	case "gcp":
		instances, err = parseGCP(output)
	case "azure":
		instances, err = parseAzure(output)
	}
	if err != nil {
		return nil, fmt.Errorf("%s inventory: %w", i.Provider, err)
	}
	sort.Slice(instances, func(a, b int) bool {
		if instances[a].Region != instances[b].Region {
			return instances[a].Region < instances[b].Region
		}
		return instances[a].ID < instances[b].ID
	})
	return instances, nil
}

func parseAWS(raw []byte) ([]Instance, error) {
	var body struct {
		Reservations []struct {
			Instances []struct {
				ID      string `json:"InstanceId"`
				Type    string `json:"InstanceType"`
				Private string `json:"PrivateIpAddress"`
				Public  string `json:"PublicIpAddress"`
				State   struct {
					Name string `json:"Name"`
				} `json:"State"`
				Placement struct {
					Zone string `json:"AvailabilityZone"`
				} `json:"Placement"`
				Tags []struct {
					Key   string `json:"Key"`
					Value string `json:"Value"`
				} `json:"Tags"`
			} `json:"Instances"`
		} `json:"Reservations"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	var out []Instance
	for _, reservation := range body.Reservations {
		for _, vm := range reservation.Instances {
			if vm.ID == "" || vm.State.Name == "terminated" {
				continue
			}
			name := vm.ID
			for _, tag := range vm.Tags {
				if tag.Key == "Name" && tag.Value != "" {
					name = tag.Value
				}
			}
			out = append(out, Instance{Provider: "aws", ID: vm.ID, Name: name, Region: vm.Placement.Zone, Type: vm.Type, State: vm.State.Name, Private: vm.Private, Public: vm.Public})
		}
	}
	return out, nil
}

func parseGCP(raw []byte) ([]Instance, error) {
	var body []struct {
		ID                string `json:"id"`
		Name              string `json:"name"`
		Zone              string `json:"zone"`
		MachineType       string `json:"machineType"`
		Status            string `json:"status"`
		NetworkInterfaces []struct {
			NetworkIP     string `json:"networkIP"`
			AccessConfigs []struct {
				NatIP string `json:"natIP"`
			} `json:"accessConfigs"`
		} `json:"networkInterfaces"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	var out []Instance
	for _, vm := range body {
		if vm.Name == "" || vm.Status == "TERMINATED" {
			continue
		}
		id := vm.ID
		if id == "" {
			id = last(vm.Zone) + "/" + vm.Name
		}
		item := Instance{Provider: "gcp", ID: id, Name: vm.Name, Region: last(vm.Zone), Type: last(vm.MachineType), State: strings.ToLower(vm.Status)}
		if len(vm.NetworkInterfaces) > 0 {
			item.Private = vm.NetworkInterfaces[0].NetworkIP
			if len(vm.NetworkInterfaces[0].AccessConfigs) > 0 {
				item.Public = vm.NetworkInterfaces[0].AccessConfigs[0].NatIP
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func parseAzure(raw []byte) ([]Instance, error) {
	var body []struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		Location        string `json:"location"`
		PowerState      string `json:"powerState"`
		PrivateIPs      string `json:"privateIps"`
		PublicIPs       string `json:"publicIps"`
		HardwareProfile struct {
			VMSize string `json:"vmSize"`
		} `json:"hardwareProfile"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	var out []Instance
	for _, vm := range body {
		if vm.ID == "" {
			continue
		}
		out = append(out, Instance{Provider: "azure", ID: vm.ID, Name: vm.Name, Region: vm.Location, Type: vm.HardwareProfile.VMSize, State: vm.PowerState, Private: vm.PrivateIPs, Public: vm.PublicIPs})
	}
	return out, nil
}

func last(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	return parts[len(parts)-1]
}
