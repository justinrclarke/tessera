package k8simport

import (
	"fmt"

	"tessera/internal/api"
)

func serviceRoutes(doc map[string]any, docs []map[string]any) ([]api.Route, string) {
	if str(doc["kind"]) == "Ingress" {
		return nil, "HTTP host/path routing and TLS are not converted to TCP Routes; import the backend Service"
	}
	spec := mapOf(doc["spec"])
	if kind := str(spec["type"]); kind != "" && kind != "ClusterIP" {
		return nil, "Service type " + kind + " is not converted"
	}
	if str(spec["clusterIP"]) == "None" || str(spec["sessionAffinity"]) == "ClientIP" || str(spec["internalTrafficPolicy"]) == "Local" {
		return nil, "headless, session-affine, or node-local Services are not converted"
	}
	selector := stringMap(spec["selector"])
	if len(selector) == 0 {
		return nil, "Services without selectors are not converted"
	}
	namespace := namespaceOf(doc)
	var matches []map[string]any
	for _, candidate := range docs {
		kind := str(candidate["kind"])
		if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" && kind != "Job" || namespaceOf(candidate) != namespace {
			continue
		}
		labels := stringMap(mapOf(mapOf(mapOf(candidate["spec"])["template"])["metadata"])["labels"])
		match := true
		for key, value := range selector {
			if got, ok := labels[key]; !ok || got != value {
				match = false
			}
		}
		if match {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Sprintf("selector matches %d workloads in this import; exactly one is required", len(matches))
	}
	work := matches[0]
	if reason := unsupportedWorkload(work); reason != "" {
		return nil, "selected workload is not converted: " + reason
	}
	containers := sliceOf(mapOf(mapOf(mapOf(work["spec"])["template"])["spec"])["containers"])
	if len(containers) != 1 {
		return nil, "selected workload must have one container"
	}
	named := map[string]int{}
	for _, port := range sliceOf(mapOf(containers[0])["ports"]) {
		p := mapOf(port)
		if str(p["name"]) != "" {
			named[str(p["name"])] = int(num(p["containerPort"]))
		}
	}
	var result []api.Route
	for _, item := range sliceOf(spec["ports"]) {
		p := mapOf(item)
		if protocol := str(p["protocol"]); protocol != "" && protocol != "TCP" {
			return nil, "non-TCP Services are not converted"
		}
		listen, target := int(num(p["port"])), int(num(p["targetPort"]))
		if name, ok := p["targetPort"].(string); ok {
			target = named[name]
			if target == 0 {
				return nil, "unresolved named targetPort " + name
			}
		} else if p["targetPort"] == nil {
			target = listen
		}
		if listen < 1 || listen > 65535 || target < 1 || target > 65535 {
			return nil, "invalid TCP port"
		}
		result = append(result, api.Route{Kind: api.KindRoute, Name: metaName(doc), App: metaName(work), Port: listen, TargetPort: target})
	}
	if len(result) == 0 {
		return nil, "no ports"
	}
	return result, ""
}

func namespaceOf(doc map[string]any) string {
	if namespace := str(mapOf(doc["metadata"])["namespace"]); namespace != "" {
		return namespace
	}
	return "default"
}
