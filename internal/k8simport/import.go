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
	seen := map[string]bool{}
	ports := map[int]string{}
	routeNames := map[string]bool{}
	for _, doc := range docs {
		kind := str(doc["kind"])
		key := kind + "/" + metaName(doc)
		if kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet" || kind == "Job" {
			key = "workload/" + metaName(doc)
		}
		if seen[key] {
			return Result{}, fmt.Errorf("duplicate converted name %s", key)
		}
		seen[key] = true
		if kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet" || kind == "Job" {
			if reason := unsupportedWorkload(doc); reason != "" {
				res.Skipped = append(res.Skipped, kind+"/"+metaName(doc)+": "+reason)
				continue
			}
		}
		if kind == "Service" || kind == "Ingress" {
			routes, reason := serviceRoutes(doc, docs)
			if reason != "" {
				res.Skipped = append(res.Skipped, kind+"/"+metaName(doc)+": "+reason)
			}
			for i := range routes {
				r := routes[i]
				if i > 0 {
					r.Name = fmt.Sprintf("%s-%d", r.Name, r.Port)
				}
				if prev := ports[r.Port]; prev != "" {
					return Result{}, fmt.Errorf("Routes %s and %s require the same listen port %d; assign distinct ports before import", prev, r.Name, r.Port)
				}
				ports[r.Port] = r.Name
				if routeNames[r.Name] {
					return Result{}, fmt.Errorf("duplicate generated Route name %s", r.Name)
				}
				routeNames[r.Name] = true
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
	for _, obj := range res.Objects {
		if obj.Route == nil {
			continue
		}
		for _, target := range res.Objects {
			if target.App == nil || target.App.Name != obj.Route.App {
				continue
			}
			found := false
			for _, port := range target.App.Ports {
				found = found || port.Container == obj.Route.TargetPort
			}
			if !found {
				target.App.Ports = append(target.App.Ports, api.Port{Container: obj.Route.TargetPort})
			}
		}
	}
	if len(res.Objects) == 0 {
		return res, nil
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	for _, obj := range export(res.Objects) {
		if err := encoder.Encode(obj); err != nil {
			return Result{}, err
		}
	}
	err = encoder.Close()
	if err != nil {
		return Result{}, err
	}
	res.YAML = output.String()
	if len(res.Objects) > 0 {
		if _, err := api.DecodeAll([]byte(res.YAML)); err != nil {
			return Result{}, fmt.Errorf("converted manifest: %w", err)
		}
	}
	return res, nil
}

func unsupportedWorkload(doc map[string]any) string {
	spec := mapOf(doc["spec"])
	switch str(doc["kind"]) {
	case "DaemonSet":
		return "per-node DaemonSet placement is not converted"
	case "StatefulSet":
		return "stable StatefulSet identity and ordered updates are not converted"
	case "Job":
		parallel, complete := 1, 1
		if spec["parallelism"] != nil {
			parallel = int(num(spec["parallelism"]))
		}
		if spec["completions"] != nil {
			complete = int(num(spec["completions"]))
		} else if parallel > 1 {
			return "work-queue Job completion semantics are not converted"
		}
		if parallel != complete || parallel < 1 {
			return "Jobs with different parallelism and completions are not converted"
		}
		for _, field := range []string{"completionMode", "suspend", "backoffLimit", "activeDeadlineSeconds", "podFailurePolicy", "successPolicy", "backoffLimitPerIndex", "maxFailedIndexes"} {
			if spec[field] != nil {
				return "Job " + field + " is not converted"
			}
		}
	}
	pod := mapOf(mapOf(spec["template"])["spec"])
	if len(sliceOf(spec["volumeClaimTemplates"])) > 0 || len(sliceOf(pod["volumes"])) > 0 {
		return "storage volumes are not converted"
	}
	if len(sliceOf(pod["containers"])) > 1 || len(sliceOf(pod["initContainers"])) > 0 {
		return "sidecars and init containers are not converted"
	}
	for _, field := range []string{"securityContext", "serviceAccountName", "hostNetwork", "hostPID", "hostIPC", "affinity", "tolerations", "topologySpreadConstraints", "nodeName", "imagePullSecrets", "dnsConfig", "schedulerName", "runtimeClassName"} {
		if value := pod[field]; value != nil && value != false && value != "" {
			return field + " is not converted"
		}
	}
	for _, item := range sliceOf(pod["containers"]) {
		container := mapOf(item)
		if len(sliceOf(container["args"])) > 0 && len(sliceOf(container["command"])) == 0 {
			return "arguments without an explicit command are not converted"
		}
		for _, port := range sliceOf(container["ports"]) {
			p := mapOf(port)
			if protocol := str(p["protocol"]); protocol != "" && protocol != "TCP" {
				return "non-TCP container ports are not converted"
			}
		}
		resources := mapOf(container["resources"])
		requests, limits := mapOf(resources["requests"]), mapOf(resources["limits"])
		for resource := range requests {
			if resource != "cpu" && resource != "memory" && resource != "nvidia.com/gpu" {
				return "resource " + resource + " is not converted"
			}
		}
		for resource, limit := range limits {
			if str(limit) != str(requests[resource]) {
				return "distinct resource requests and limits are not converted"
			}
		}
		for _, field := range []string{"volumeMounts", "volumeDevices", "envFrom", "readinessProbe", "livenessProbe", "startupProbe", "securityContext", "lifecycle"} {
			if container[field] != nil {
				return field + " is not converted"
			}
		}
		for _, env := range sliceOf(container["env"]) {
			if mapOf(env)["valueFrom"] != nil {
				return "environment references are not converted"
			}
		}
	}
	return ""
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
		docs = append(docs, fromJSON(doc)...)
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
		if len(mapOf(doc["binaryData"])) > 0 || doc["immutable"] == true {
			return api.Object{}, "ConfigMap/" + metaName(doc) + ": binary or immutable data is not converted", nil
		}
		c := api.Config{Kind: api.KindConfig, Name: metaName(doc), Data: stringMap(doc["data"])}
		return api.Object{Kind: api.KindConfig, Config: &c}, "", nil
	case "Secret":
		if kind := str(doc["type"]); kind != "" && kind != "Opaque" || doc["immutable"] == true {
			return api.Object{}, "Secret/" + metaName(doc) + ": typed or immutable Secrets are not converted", nil
		}
		data := map[string]string{}
		for key, value := range mapOf(doc["data"]) {
			decoded, err := base64.StdEncoding.DecodeString(str(value))
			if err != nil {
				return api.Object{}, "", fmt.Errorf("Secret/%s: invalid base64 data for %s", metaName(doc), key)
			}
			data[key] = string(decoded)
		}
		for key, value := range stringMap(doc["stringData"]) {
			data[key] = value
		}
		sec := api.Secret{Kind: api.KindSecret, Name: metaName(doc), Data: data}
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
		Kind:       kind,
		Name:       metaName(doc),
		Image:      str(c["image"]),
		Replicas:   int(num(spec["replicas"])),
		Command:    append(strSlice(c["command"]), strSlice(c["args"])...),
		Env:        envOf(c["env"]),
		NodeLabels: stringMap(tmpl["nodeSelector"]),
	}
	if spec["replicas"] == nil {
		if kind == api.KindJob {
			app.Replicas = int(num(spec["parallelism"]))
		}
		if app.Replicas == 0 {
			app.Replicas = 1
		}
	}
	for _, p := range sliceOf(c["ports"]) {
		pm := mapOf(p)
		app.Ports = append(app.Ports, api.Port{Container: int(num(pm["containerPort"])), Host: int(num(pm["hostPort"]))})
	}
	req := mapOf(mapOf(c["resources"])["requests"])
	cpu, err := api.ParseMilliCPU(req["cpu"])
	if err != nil {
		return api.App{}, err
	}
	mem, err := api.ParseMemory(req["memory"])
	if err != nil {
		return api.App{}, err
	}
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
	meta := mapOf(doc["metadata"])
	name := str(meta["name"])
	if namespace := str(meta["namespace"]); namespace != "" && namespace != "default" {
		return namespace + "." + name
	}
	return name
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
