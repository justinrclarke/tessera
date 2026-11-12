package agent

import (
	"tessera/internal/client"
	"tessera/internal/runtime"
)

func (a *Agent) clientWithToken(token string) *client.Client {
	cl := client.New(a.Client.Endpoint(), token)
	cl.SetControllers(a.Client.Controllers())
	cl.HTTP = a.Client.HTTP
	return cl
}

func (a *Agent) runtimeKind() string {
	if kind, ok := a.Runtime.(interface{ Kind() string }); ok {
		return kind.Kind()
	}
	switch a.Runtime.(type) {
	case *runtime.CTR:
		return "ctr"
	case *runtime.Docker:
		return "docker"
	case *runtime.Fake:
		return "fake"
	}
	return ""
}
