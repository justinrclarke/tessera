package api

import (
	"bytes"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
		if r.Name == "" || r.App == "" || r.Port < 1 || r.Port > 65535 || r.TargetPort < 0 || r.TargetPort > 65535 {
			return Object{}, fmt.Errorf("Route requires name, app, a listen port between 1 and 65535, and an optional valid target_port")
		}
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
	if app.Storage != nil {
		if err := app.Storage.Validate(); err != nil {
			return App{}, err
		}
		if app.Replicas > 1 || app.Kind != KindApp || app.Gang || app.Checkpoint != nil {
			return App{}, fmt.Errorf("storage requires a single-replica App without checkpointing")
		}
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
		h := app.Health
		u, err := url.ParseRequestURI(h.Path)
		if err != nil || !strings.HasPrefix(h.Path, "/") || strings.HasPrefix(h.Path, "//") || u.Host != "" || u.Fragment != "" || app.Kind == KindJob {
			return App{}, fmt.Errorf("app %s: health requires an HTTP path on an App", app.Name)
		}
		matched := false
		for _, p := range app.Ports {
			matched = matched || h.Port == p.Container
		}
		if !matched {
			return App{}, fmt.Errorf("app %s: health port must match a published container port", app.Name)
		}
		for _, value := range []string{h.Timeout, h.StartupTimeout} {
			if value != "" {
				d, err := time.ParseDuration(value)
				if err != nil || d <= 0 {
					return App{}, fmt.Errorf("app %s: health timeouts must be positive durations", app.Name)
				}
			}
		}
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
	if app.Checkpoint != nil {
		c := app.Checkpoint
		if app.Kind != KindJob || !app.Gang || !filepath.IsAbs(c.Directory) || filepath.Clean(c.Directory) == "/" || strings.ContainsAny(c.Directory, "\x00\r\n,:") {
			return App{}, fmt.Errorf("app %s: checkpoint requires a gang Job and an absolute shared directory other than /", app.Name)
		}
		if c.Timeout != "" {
			if d, err := time.ParseDuration(c.Timeout); err != nil || d <= 0 {
				return App{}, fmt.Errorf("app %s: checkpoint timeout must be a positive duration", app.Name)
			}
		}
	}
	if app.DrainTimeout != "" {
		d, err := time.ParseDuration(app.DrainTimeout)
		if err != nil || d <= 0 || d > time.Hour || app.Kind == KindJob {
			return App{}, fmt.Errorf("app %s: drain_timeout requires an App and a positive duration no greater than one hour", app.Name)
		}
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
	for key := range raw {
		if key != "kind" && key != "name" && key != "data" {
			return namedData{}, fmt.Errorf("%s: unknown field %q", kind, key)
		}
	}
	name, _ := raw["name"].(string)
	if name == "" {
		return namedData{}, fmt.Errorf("%s name is required", kind)
	}
	data := map[string]string{}
	switch d := raw["data"].(type) {
	case map[string]any:
		for k, v := range d {
			switch v.(type) {
			case string, int, int64, float64, bool:
			default:
				return namedData{}, fmt.Errorf("%s %s: data values must be scalars", kind, name)
			}
			data[k] = scalarString(v)
		}
	case map[string]string:
		data = d
	case nil:
	default:
		return namedData{}, fmt.Errorf("%s %s: data must be a mapping", kind, name)
	}
	return namedData{name: name, data: data}, nil
}

func decodePolicy(raw map[string]any) (Policy, error) {
	mv, _ := raw["move"].(map[string]any)
	if raw["move"] != nil && mv == nil {
		return Policy{}, fmt.Errorf("Policy move must contain min_gain or cooldown")
	}
	delete(raw, "move")
	var p Policy
	if err := remarshal(raw, &p); err != nil {
		return Policy{}, err
	}
	p.Kind = KindPolicy
	if mv != nil {
		for key := range mv {
			if key != "min_gain" && key != "cooldown" {
				return Policy{}, fmt.Errorf("Policy move: unknown field %q", key)
			}
		}
		if g, ok := mv["min_gain"]; ok {
			fmtS := scalarString(g)
			f, err := strconv.ParseFloat(fmtS, 64)
			if err != nil {
				return Policy{}, fmt.Errorf("Policy move min_gain must be a finite nonnegative number")
			}
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
	if math.IsNaN(p.MoveMinGain) || math.IsInf(p.MoveMinGain, 0) || p.MoveMinGain < 0 || p.MaxRestarts < 0 {
		return Policy{}, fmt.Errorf("Policy gain and restart limit must be finite and nonnegative")
	}
	for _, value := range []string{p.MoveCooldown, p.SoloPromotion, p.SuspectAfter, p.DeadAfter, p.LeaseTTL} {
		if value != "" {
			if d, err := time.ParseDuration(value); err != nil || d <= 0 {
				return Policy{}, fmt.Errorf("Policy timing settings must be positive durations")
			}
		}
	}
	if p.Dead() <= p.Suspect() {
		return Policy{}, fmt.Errorf("Policy dead_after must exceed suspect_after")
	}
	for _, action := range append(append([]string(nil), p.Auto...), p.Confirm...) {
		switch action {
		case "restart", "reschedule", "cordon", "uncordon", "rollback", "prune", "renew", "move", "hold", "delete", "wipe", "reimage":
		default:
			return Policy{}, fmt.Errorf("Policy: unknown action %q", action)
		}
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
