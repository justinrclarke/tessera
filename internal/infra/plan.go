package infra

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"sort"

	"tessera/internal/api"

	"gopkg.in/yaml.v3"
)

type Network struct {
	Name     string `json:"name" yaml:"name"`
	Provider string `json:"provider" yaml:"provider"`
	Region   string `json:"region" yaml:"region"`
	CIDR     string `json:"cidr" yaml:"cidr"`
}

type Machine struct {
	Name     string `json:"name" yaml:"name"`
	Provider string `json:"provider" yaml:"provider"`
	Region   string `json:"region" yaml:"region"`
	Type     string `json:"type" yaml:"type"`
	Image    string `json:"image" yaml:"image"`
	Network  string `json:"network" yaml:"network"`
}

type Desired struct {
	Networks []Network `json:"networks" yaml:"networks"`
	Machines []Machine `json:"machines" yaml:"machines"`
	Apps     []api.App `json:"apps" yaml:"apps"`
}

type ExistingNetwork struct {
	Network Network `json:"network"`
	Owned   bool    `json:"owned"`
}

type ExistingMachine struct {
	Machine Machine `json:"machine"`
	Owned   bool    `json:"owned"`
}

type ExistingApp struct {
	App   api.App `json:"app"`
	Owned bool    `json:"owned"`
}

type State struct {
	Networks []ExistingNetwork `json:"networks"`
	Machines []ExistingMachine `json:"machines"`
	Apps     []ExistingApp     `json:"apps"`
}

type Action struct {
	Operation string `json:"operation"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
}

type Result struct {
	Generation string   `json:"generation"`
	Actions    []Action `json:"actions"`
}

func Parse(raw []byte) (Desired, error) {
	var file struct {
		Networks []Network        `yaml:"networks"`
		Machines []Machine        `yaml:"machines"`
		Apps     []map[string]any `yaml:"apps"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return Desired{}, err
	}
	desired := Desired{Networks: file.Networks, Machines: file.Machines}
	for _, appRaw := range file.Apps {
		if appRaw["kind"] == nil {
			appRaw["kind"] = api.KindApp
		}
		encoded, err := yaml.Marshal(appRaw)
		if err != nil {
			return Desired{}, err
		}
		obj, err := api.DecodeOne(encoded)
		if err != nil {
			return Desired{}, err
		}
		if obj.App == nil {
			return Desired{}, fmt.Errorf("infrastructure apps must be App, Job, or Model")
		}
		desired.Apps = append(desired.Apps, *obj.App)
	}
	return desired, desired.Validate()
}

func (d Desired) Validate() error {
	providers := map[string]bool{"aws": true, "gcp": true, "azure": true}
	networks := map[string]Network{}
	for _, n := range d.Networks {
		if n.Name == "" || n.Region == "" || n.CIDR == "" || !providers[n.Provider] {
			return fmt.Errorf("network %q requires name, provider, region, and cidr", n.Name)
		}
		if _, _, err := net.ParseCIDR(n.CIDR); err != nil {
			return fmt.Errorf("network %q has invalid cidr %q: %w", n.Name, n.CIDR, err)
		}
		if _, exists := networks[n.Name]; exists {
			return fmt.Errorf("duplicate network %q", n.Name)
		}
		networks[n.Name] = n
	}
	machines := map[string]bool{}
	for _, m := range d.Machines {
		if m.Name == "" || m.Region == "" || m.Type == "" || m.Image == "" || !providers[m.Provider] {
			return fmt.Errorf("machine %q requires name, provider, region, type, and image", m.Name)
		}
		if machines[m.Name] {
			return fmt.Errorf("duplicate machine %q", m.Name)
		}
		machines[m.Name] = true
		if m.Network != "" {
			network, exists := networks[m.Network]
			if !exists || network.Provider != m.Provider || network.Region != m.Region {
				return fmt.Errorf("machine %q references an incompatible network %q", m.Name, m.Network)
			}
		}
	}
	apps := map[string]bool{}
	for _, app := range d.Apps {
		if app.Name == "" || app.Image == "" || apps[app.Name] {
			return fmt.Errorf("app %q requires a unique name and image", app.Name)
		}
		apps[app.Name] = true
	}
	return nil
}

func Plan(desired Desired, current State) (Result, error) {
	if err := desired.Validate(); err != nil {
		return Result{}, err
	}
	canonicalDesired := Desired{
		Networks: append([]Network(nil), desired.Networks...),
		Machines: append([]Machine(nil), desired.Machines...),
		Apps:     append([]api.App(nil), desired.Apps...),
	}
	sort.Slice(canonicalDesired.Networks, func(a, b int) bool { return canonicalDesired.Networks[a].Name < canonicalDesired.Networks[b].Name })
	sort.Slice(canonicalDesired.Machines, func(a, b int) bool { return canonicalDesired.Machines[a].Name < canonicalDesired.Machines[b].Name })
	sort.Slice(canonicalDesired.Apps, func(a, b int) bool { return canonicalDesired.Apps[a].Name < canonicalDesired.Apps[b].Name })
	canonical, err := json.Marshal(canonicalDesired)
	if err != nil {
		return Result{}, err
	}
	hash := sha256.Sum256(canonical)
	result := Result{Generation: hex.EncodeToString(hash[:])}
	currentNetworks := map[string]ExistingNetwork{}
	currentMachines := map[string]ExistingMachine{}
	currentApps := map[string]ExistingApp{}
	for _, n := range current.Networks {
		if n.Network.Name == "" || currentNetworks[n.Network.Name].Network.Name != "" {
			return Result{}, fmt.Errorf("duplicate or unnamed current network %q", n.Network.Name)
		}
		currentNetworks[n.Network.Name] = n
	}
	for _, m := range current.Machines {
		if m.Machine.Name == "" || currentMachines[m.Machine.Name].Machine.Name != "" {
			return Result{}, fmt.Errorf("duplicate or unnamed current machine %q", m.Machine.Name)
		}
		currentMachines[m.Machine.Name] = m
	}
	for _, app := range current.Apps {
		if app.App.Name == "" || currentApps[app.App.Name].App.Name != "" {
			return Result{}, fmt.Errorf("duplicate or unnamed current app %q", app.App.Name)
		}
		currentApps[app.App.Name] = app
	}
	wantedNetworks := map[string]bool{}
	wantedMachines := map[string]bool{}
	wantedApps := map[string]bool{}
	for _, network := range desired.Networks {
		wantedNetworks[network.Name] = true
		if existing, ok := currentNetworks[network.Name]; !ok {
			result.Actions = append(result.Actions, Action{"create", "network", network.Name, "missing"})
		} else if existing.Network != network {
			result.Actions = append(result.Actions, Action{"confirm", "network", network.Name, "network definition changed"})
		}
	}
	for _, machine := range desired.Machines {
		wantedMachines[machine.Name] = true
		if existing, ok := currentMachines[machine.Name]; !ok {
			result.Actions = append(result.Actions, Action{"create", "machine", machine.Name, "missing"})
		} else if existing.Machine != machine {
			result.Actions = append(result.Actions, Action{"confirm", "machine", machine.Name, "machine definition changed"})
		}
	}
	for _, app := range desired.Apps {
		wantedApps[app.Name] = true
		if existing, ok := currentApps[app.Name]; !ok {
			result.Actions = append(result.Actions, Action{"apply", "app", app.Name, "missing"})
		} else if !equalApp(existing.App, app) {
			result.Actions = append(result.Actions, Action{"apply", "app", app.Name, "app definition changed"})
		}
	}
	for name, app := range currentApps {
		if app.Owned && !wantedApps[name] {
			result.Actions = append(result.Actions, Action{"confirm", "app", name, "removed from desired file"})
		}
	}
	for name, machine := range currentMachines {
		if machine.Owned && !wantedMachines[name] {
			result.Actions = append(result.Actions, Action{"confirm", "machine", name, "removed from desired file"})
		}
	}
	for name, network := range currentNetworks {
		if network.Owned && !wantedNetworks[name] {
			result.Actions = append(result.Actions, Action{"confirm", "network", name, "removed from desired file"})
		}
	}
	sort.SliceStable(result.Actions, func(a, b int) bool {
		aOrder := actionOrder(result.Actions[a])
		bOrder := actionOrder(result.Actions[b])
		if aOrder != bOrder {
			return aOrder < bOrder
		}
		return result.Actions[a].Name < result.Actions[b].Name
	})
	return result, nil
}

func equalApp(a, b api.App) bool {
	a.Generation, b.Generation = 0, 0
	a.HealthyGeneration, b.HealthyGeneration = 0, 0
	return reflect.DeepEqual(a, b)
}

func actionOrder(a Action) int {
	if a.Operation == "confirm" && a.Reason == "removed from desired file" {
		switch a.Kind {
		case "app":
			return 3
		case "machine":
			return 4
		default:
			return 5
		}
	}
	switch a.Kind {
	case "network":
		return 0
	case "machine":
		return 1
	default:
		return 2
	}
}
