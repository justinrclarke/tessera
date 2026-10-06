package api

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Object struct {
	Kind   string
	App    *App
	Route  *Route
	Config *Config
	Secret *Secret
	Policy *Policy
}

func DecodeAll(b []byte) ([]Object, error) {
	parts := splitDocs(b)
	var out []Object
	for _, p := range parts {
		obj, err := DecodeOne(p)
		if err != nil {
			return nil, err
		}
		out = append(out, obj)
	}
	return out, nil
}

func DecodeOne(b []byte) (Object, error) {
	var raw map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&raw); err != nil {
		return Object{}, err
	}
	if len(raw) == 0 {
		return Object{}, fmt.Errorf("empty document")
	}
	kind, _ := raw["kind"].(string)
	if kind == "" {
		if _, ok := raw["apiVersion"]; ok {
			kind, _ = raw["kind"].(string)
		}
	}
	switch kind {
	case KindApp, KindJob, KindModel, "":
		app, err := decodeApp(raw, kind)
		if err != nil {
			return Object{}, err
		}
		return Object{Kind: app.Kind, App: &app}, nil
	case KindRoute:
		var r Route
		if err := remarshal(raw, &r); err != nil {
			return Object{}, err
		}
		r.Kind = KindRoute
		return Object{Kind: KindRoute, Route: &r}, nil
	case KindConfig:
		c, err := decodeMapKind(raw, KindConfig)
		if err != nil {
			return Object{}, err
		}
		return Object{Kind: KindConfig, Config: &Config{Kind: KindConfig, Name: c.name, Data: c.data}}, nil
	case KindSecret:
		c, err := decodeMapKind(raw, KindSecret)
		if err != nil {
			return Object{}, err
		}
		return Object{Kind: KindSecret, Secret: &Secret{Kind: KindSecret, Name: c.name, Data: c.data}}, nil
	case KindPolicy:
		p, err := decodePolicy(raw)
		if err != nil {
			return Object{}, err
		}
		return Object{Kind: KindPolicy, Policy: &p}, nil
	default:
		return Object{}, fmt.Errorf("unknown kind %q", kind)
	}
}

func decodeApp(raw map[string]any, kind string) (App, error) {
	if kind == "" {
		kind = KindApp
	}
	if kind == KindModel {
		kind = KindApp
		if raw["sensitive_to"] == nil {
			raw["sensitive_to"] = "gpu"
		}
	}
	var resRaw any
	var gpuMemoryRaw any
	if v, ok := raw["resources"]; ok {
		resRaw = v
		delete(raw, "resources")
	}
	if v, ok := raw["gpu_memory"]; ok {
		gpuMemoryRaw = v
		delete(raw, "gpu_memory")
	}
	var app App
	if err := remarshal(raw, &app); err != nil {
		return App{}, err
	}
	app.Kind = kind
	if _, specified := raw["replicas"]; !specified {
		app.Replicas = 1
	}
	if app.Replicas < 0 {
		return App{}, fmt.Errorf("app %s: replicas cannot be negative", app.Name)
	}
	if app.Name == "" {
		return App{}, fmt.Errorf("app name is required")
	}
	if app.Image == "" {
		return App{}, fmt.Errorf("app %s: image is required", app.Name)
	}
	if res, ok := resRaw.(map[string]any); ok {
		for key := range res {
			if key != "cpu" && key != "memory" {
				return App{}, fmt.Errorf("app %s: unknown resource %q", app.Name, key)
			}
		}
		cpu, err := ParseMilliCPU(res["cpu"])
		if err != nil {
			return App{}, err
		}
		mem, err := ParseMemory(res["memory"])
		if err != nil {
			return App{}, err
		}
		app.Resources = Resources{CPU: cpu, Memory: mem}
	} else if resRaw != nil {
		return App{}, fmt.Errorf("app %s: resources must contain cpu or memory", app.Name)
	}
	if app.Resources.CPU < 0 || app.Resources.Memory < 0 {
		return App{}, fmt.Errorf("app %s: resources cannot be negative", app.Name)
	}
	if app.Health != nil {
		return App{}, fmt.Errorf("app %s: health probes are not implemented", app.Name)
	}
	for _, port := range app.Ports {
		if port.Container < 1 || port.Container > 65535 || port.Host < 0 || port.Host > 65535 || (port.Protocol != "" && !strings.EqualFold(port.Protocol, "tcp")) {
			return App{}, fmt.Errorf("app %s: ports require TCP and numbers between 1 and 65535", app.Name)
		}
	}
	if gpuMemoryRaw != nil {
		gpuMemory, err := ParseMemory(gpuMemoryRaw)
		if err != nil {
			return App{}, err
		}
		app.GPUMemory = gpuMemory
	}
	if app.GPUs < 0 || app.GPUMemory < 0 {
		return App{}, fmt.Errorf("app %s: GPU requests cannot be negative", app.Name)
	}
	if app.GPUs == 0 && (app.GPUModel != "" || app.GPUMemory > 0) {
		return App{}, fmt.Errorf("app %s: gpu_model and gpu_memory require gpus", app.Name)
	}
	if app.GangFabric != "" && (!app.Gang || app.Kind != KindJob) {
		return App{}, fmt.Errorf("app %s: gang_fabric requires a gang Job", app.Name)
	}
	if app.SensitiveTo == "" && app.GPUs > 0 {
		app.SensitiveTo = "gpu"
	}
	return app, nil
}

type namedData struct {
	name string
	data map[string]string
}

func decodeMapKind(raw map[string]any, kind string) (namedData, error) {
	name, _ := raw["name"].(string)
	if name == "" {
		return namedData{}, fmt.Errorf("%s name is required", kind)
	}
	data := map[string]string{}
	switch d := raw["data"].(type) {
	case map[string]any:
		for k, v := range d {
			data[k] = scalarString(v)
		}
	case map[string]string:
		data = d
	}
	return namedData{name: name, data: data}, nil
}

func decodePolicy(raw map[string]any) (Policy, error) {
	mv, _ := raw["move"].(map[string]any)
	delete(raw, "move")
	var p Policy
	if err := remarshal(raw, &p); err != nil {
		return Policy{}, err
	}
	p.Kind = KindPolicy
	if mv != nil {
		if g, ok := mv["min_gain"]; ok {
			fmtS := scalarString(g)
			var f float64
			fmt.Sscan(fmtS, &f)
			p.MoveMinGain = f
		}
		if c, ok := mv["cooldown"]; ok {
			p.MoveCooldown = scalarString(c)
		}
	}
	if p.MoveMinGain == 0 {
		p.MoveMinGain = 0.15
	}
	if p.MoveCooldown == "" {
		p.MoveCooldown = "10m"
	}
	if p.SoloPromotion == "" {
		p.SoloPromotion = "30s"
	}
	if p.MaxRestarts == 0 {
		p.MaxRestarts = 3
	}
	return p, nil
}

func remarshal(raw map[string]any, dest any) error {
	b, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	decoder.KnownFields(true)
	return decoder.Decode(dest)
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func splitDocs(b []byte) [][]byte {
	s := string(b)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	chunks := strings.Split(s, "\n---\n")
	var out [][]byte
	for _, c := range chunks {
		c = strings.TrimSpace(c)
		if c == "" || c == "---" {
			continue
		}
		out = append(out, []byte(c))
	}
	return out
}
