package k8simport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"tessera/internal/api"
)

type Result struct {
	Objects []api.Object
	Skipped []string
	YAML    string
}

type ClusterOptions struct {
	Kubeconfig string
	Context    string
	Namespace  string
	Run        func([]string) ([]byte, error)
}

func Convert(b []byte) (Result, error) {
	docs, err := documents(b)
	if err != nil {
		return Result{}, err
	}
	var res Result
	for _, doc := range docs {
		kind := str(doc["kind"])
		if kind == "Service" || kind == "Ingress" {
			routes := routes(doc)
			if len(routes) == 0 {
				res.Skipped = append(res.Skipped, kind+"/"+metaName(doc)+": no ports")
			}
			for i := range routes {
				r := routes[i]
				if i > 0 {
					r.Name = fmt.Sprintf("%s-%d", r.Name, r.Port)
				}
				res.Objects = append(res.Objects, api.Object{Kind: api.KindRoute, Route: &r})
			}
			continue
		}
		obj, skip, err := one(doc)
		if err != nil {
			return Result{}, err
		}
		if skip != "" {
			res.Skipped = append(res.Skipped, skip)
			continue
		}
		if obj.Kind != "" {
			res.Objects = append(res.Objects, obj)
		}
	}
	raw, err := yaml.Marshal(export(res.Objects))
	if err != nil {
		return Result{}, err
	}
	res.YAML = string(raw)
	return res, nil
}

func FromCluster(kubeconfig string) (Result, error) {
	return FromClusterOptions(ClusterOptions{Kubeconfig: kubeconfig})
}

func FromClusterOptions(opts ClusterOptions) (Result, error) {
	args := []string{"get", "deployments,statefulsets,daemonsets,jobs,services,ingresses,configmaps,secrets"}
	if opts.Namespace == "" {
		args = append(args, "-A")
	} else {
		args = append(args, "-n", opts.Namespace)
	}
	args = append(args, "-o", "json")
	out, err := opts.command(args...)
	if err != nil {
		return Result{}, err
	}
	res, err := Convert(out)
	if err != nil {
		return Result{}, err
	}
	allArgs := []string{"get", "all"}
	if opts.Namespace == "" {
		allArgs = append(allArgs, "-A")
	} else {
		allArgs = append(allArgs, "-n", opts.Namespace)
	}
	allArgs = append(allArgs, "-o", "json")
	all, allErr := opts.command(allArgs...)
	if allErr != nil {
		res.Skipped = append(res.Skipped, "other Kubernetes kinds not inspected: "+allErr.Error())
	} else if docs, decodeErr := documents(all); decodeErr != nil {
		res.Skipped = append(res.Skipped, "other Kubernetes kinds not inspected: "+decodeErr.Error())
	} else {
		counts := map[string]int{}
		for _, doc := range docs {
			kind := str(doc["kind"])
			switch kind {
			case "Deployment", "StatefulSet", "DaemonSet", "Job", "Service", "Ingress", "ConfigMap", "Secret":
			default:
				counts[kind]++
			}
		}
		var kinds []string
		for kind := range counts {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %d objects not converted", kind, counts[kind]))
		}
	}
	unknown, err := opts.command("get", "customresourcedefinitions,mutatingwebhookconfigurations,validatingwebhookconfigurations", "-o", "json")
	if err != nil {
		res.Skipped = append(res.Skipped, "custom resources and webhooks not inspected: "+err.Error())
		return res, nil
	}
	var body struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(unknown, &body); err != nil {
		res.Skipped = append(res.Skipped, "custom resources and webhooks not inspected: "+err.Error())
		return res, nil
	}
	for _, item := range body.Items {
		res.Skipped = append(res.Skipped, item.Kind+"/"+item.Metadata.Name+" (not converted)")
	}
	return res, nil
}

func (opts ClusterOptions) command(args ...string) ([]byte, error) {
	flags := []string{"--request-timeout=10s"}
	if opts.Kubeconfig != "" {
		flags = append(flags, "--kubeconfig", opts.Kubeconfig)
	}
	if opts.Context != "" {
		flags = append(flags, "--context", opts.Context)
	}
	flags = append(flags, args...)
	if opts.Run != nil {
		return opts.Run(flags)
	}
	out, err := exec.Command("kubectl", flags...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func documents(b []byte) ([]map[string]any, error) {
	trim := bytes.TrimSpace(b)
	if len(trim) == 0 {
		return nil, fmt.Errorf("empty manifest")
	}
	if trim[0] == '{' || trim[0] == '[' {
		var raw any
		if err := json.Unmarshal(trim, &raw); err != nil {
			return nil, err
		}
		return fromJSON(raw), nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var docs []map[string]any
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, err
		}
		if len(doc) == 0 {
			continue
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

func fromJSON(raw any) []map[string]any {
	switch t := raw.(type) {
	case map[string]any:
		if items, ok := t["items"].([]any); ok {
			var out []map[string]any
			for _, it := range items {
				if m, ok := it.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out
		}
		return []map[string]any{t}
	case []any:
		var out []map[string]any
		for _, it := range t {
			if m, ok := it.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func one(doc map[string]any) (api.Object, string, error) {
	kind, _ := doc["kind"].(string)
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		app, err := workload(doc, api.KindApp)
		return api.Object{Kind: app.Kind, App: &app}, "", err
	case "Job":
		app, err := workload(doc, api.KindJob)
		return api.Object{Kind: app.Kind, App: &app}, "", err
	case "ConfigMap":
		c := api.Config{Kind: api.KindConfig, Name: metaName(doc), Data: stringMap(doc["data"])}
		return api.Object{Kind: api.KindConfig, Config: &c}, "", nil
	case "Secret":
		sec := api.Secret{Kind: api.KindSecret, Name: metaName(doc), Data: secretData(doc["data"])}
		return api.Object{Kind: api.KindSecret, Secret: &sec}, "", nil
	case "List":
		return api.Object{}, "", nil
	default:
		if kind == "" {
			return api.Object{}, "", fmt.Errorf("missing kind")
		}
		return api.Object{}, kind + "/" + metaName(doc), nil
	}
}

func workload(doc map[string]any, kind string) (api.App, error) {
	spec := mapOf(doc["spec"])
	tmpl := mapOf(mapOf(spec["template"])["spec"])
	containers := sliceOf(tmpl["containers"])
	if len(containers) == 0 {
		return api.App{}, fmt.Errorf("%s %s has no containers", kind, metaName(doc))
	}
	c := mapOf(containers[0])
	app := api.App{
		Kind:     kind,
		Name:     metaName(doc),
		Image:    str(c["image"]),
		Replicas: int(num(spec["replicas"])),
		Command:  strSlice(c["command"]),
		Env:      envOf(c["env"]),
	}
	if app.Replicas == 0 {
		if kind == api.KindJob {
			app.Replicas = int(num(spec["parallelism"]))
		}
		if app.Replicas == 0 {
			app.Replicas = 1
		}
	}
	if kind == api.KindJob && num(spec["completions"]) > 1 && num(spec["parallelism"]) > 1 {
		app.Gang = true
	}
	for _, p := range sliceOf(c["ports"]) {
		pm := mapOf(p)
		app.Ports = append(app.Ports, api.Port{Container: int(num(pm["containerPort"]))})
	}
	req := mapOf(mapOf(c["resources"])["requests"])
	cpu, _ := api.ParseMilliCPU(req["cpu"])
	mem, _ := api.ParseMemory(req["memory"])
	app.Resources = api.Resources{CPU: cpu, Memory: mem}
	if gpu := num(req["nvidia.com/gpu"]); gpu > 0 {
		app.GPUs = int(gpu)
		app.SensitiveTo = "gpu"
	}
	if app.Image == "" {
		return api.App{}, fmt.Errorf("%s %s has no image", kind, app.Name)
	}
	return app, nil
}

func routes(doc map[string]any) []api.Route {
	kind, _ := doc["kind"].(string)
	if kind == "Service" {
		var out []api.Route
		for _, p := range sliceOf(mapOf(doc["spec"])["ports"]) {
			pm := mapOf(p)
			out = append(out, api.Route{
				Kind: api.KindRoute, Name: metaName(doc), App: metaName(doc),
				Port: int(num(pm["port"])), TargetPort: int(num(pm["targetPort"])),
			})
		}
		return out
	}
	var out []api.Route
	for _, rule := range sliceOf(mapOf(doc["spec"])["rules"]) {
		rm := mapOf(rule)
		host := str(rm["host"])
		paths := sliceOf(mapOf(rm["http"])["paths"])
		for _, p := range paths {
			backend := mapOf(mapOf(p)["backend"])
			svc := mapOf(backend["service"])
			name := str(svc["name"])
			port := int(num(mapOf(svc["port"])["number"]))
			rname := host
			if rname == "" {
				rname = name
			}
			out = append(out, api.Route{Kind: api.KindRoute, Name: rname, App: name, Port: port, TargetPort: port})
		}
	}
	return out
}

func export(objs []api.Object) []any {
	var out []any
	for _, o := range objs {
		switch {
		case o.App != nil:
			out = append(out, o.App)
		case o.Route != nil:
			out = append(out, o.Route)
		case o.Config != nil:
			out = append(out, o.Config)
		case o.Secret != nil:
			out = append(out, o.Secret)
		case o.Policy != nil:
			out = append(out, o.Policy)
		}
	}
	return out
}

func metaName(doc map[string]any) string {
	return str(mapOf(doc["metadata"])["name"])
}

func envOf(v any) map[string]string {
	out := map[string]string{}
	for _, e := range sliceOf(v) {
		m := mapOf(e)
		if str(m["name"]) == "" {
			continue
		}
		out[str(m["name"])] = str(m["value"])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stringMap(v any) map[string]string {
	out := map[string]string{}
	for k, val := range mapOf(v) {
		out[k] = str(val)
	}
	return out
}

func secretData(v any) map[string]string {
	out := map[string]string{}
	for k, val := range mapOf(v) {
		s := str(val)
		if dec, err := base64.StdEncoding.DecodeString(s); err == nil && len(dec) > 0 {
			out[k] = string(dec)
			continue
		}
		out[k] = s
	}
	return out
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func sliceOf(v any) []any {
	s, _ := v.([]any)
	return s
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func strSlice(v any) []string {
	var out []string
	for _, i := range sliceOf(v) {
		out = append(out, str(i))
	}
	return out
}

func num(v any) float64 {
	switch t := v.(type) {
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case uint64:
		return float64(t)
	case float64:
		return t
	case float32:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		var f float64
		fmt.Sscan(strings.TrimSpace(t), &f)
		return f
	default:
		return 0
	}
}
