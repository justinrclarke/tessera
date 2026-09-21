package runtime

import (
	"context"
	"os"

	"tessera/internal/api"
)

type Spec struct {
	Name      string
	Image     string
	Command   []string
	Env       map[string]string
	Ports     []api.Port
	Resources api.Resources
	GPUs      int
}

type Container struct {
	ID       string
	Name     string
	Running  bool
	ExitCode int
	Reason   string
	OOM      bool
	HostPort int
}

type Runtime interface {
	Start(ctx context.Context, spec Spec) (Container, error)
	Stop(ctx context.Context, id string) error
	List(ctx context.Context) ([]Container, error)
	Logs(ctx context.Context, id string) (string, error)
	Prune(ctx context.Context) error
}

func Open(kind string) (Runtime, error) {
	switch kind {
	case "fake":
		return NewFake(), nil
	case "docker":
		return NewDocker(DockerSock()), nil
	case "ctr":
		return NewCTR(), nil
	default:
		if _, err := os.Stat(DockerSock()); err == nil {
			return NewDocker(DockerSock()), nil
		}
		if _, err := os.Stat("/var/run/containerd/containerd.sock"); err == nil {
			return NewCTR(), nil
		}
		return nil, os.ErrNotExist
	}
}

func DockerSock() string {
	if v := os.Getenv("DOCKER_HOST"); len(v) > 7 && v[:7] == "unix://" {
		return v[7:]
	}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return "/var/run/docker.sock"
	}
	home, _ := os.UserHomeDir()
	return home + "/.docker/run/docker.sock"
}
