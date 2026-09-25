package api

import "time"

const Version = "0.2.0"

const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusFailed    = "failed"
	StatusSucceeded = "succeeded"
	StatusStopped   = "stopped"

	NodeReady    = "ready"
	NodeSuspect  = "suspect"
	NodeDead     = "dead"
	NodeCordoned = "cordoned"

	KindApp    = "App"
	KindJob    = "Job"
	KindModel  = "Model"
	KindRoute  = "Route"
	KindConfig = "Config"
	KindSecret = "Secret"
	KindPolicy = "Policy"
)

type Resources struct {
	CPU    int64 `json:"cpu" yaml:"cpu"`
	Memory int64 `json:"memory" yaml:"memory"`
}

type Perf struct {
	CPU    float64 `json:"cpu" yaml:"cpu"`
	Memory float64 `json:"memory" yaml:"memory"`
	Disk   float64 `json:"disk" yaml:"disk"`
}

type GPU struct {
	UUID        string `json:"uuid" yaml:"uuid"`
	Model       string `json:"model" yaml:"model"`
	MemoryTotal int64  `json:"memory_total" yaml:"memory_total"`
	MemoryFree  int64  `json:"memory_free" yaml:"memory_free"`
}

type Port struct {
	Container int    `json:"container" yaml:"container"`
	Host      int    `json:"host,omitempty" yaml:"host,omitempty"`
	Protocol  string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
}

type Health struct {
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
	Port int    `json:"port,omitempty" yaml:"port,omitempty"`
}

type App struct {
	Kind              string            `json:"kind" yaml:"kind"`
	Name              string            `json:"name" yaml:"name"`
	Image             string            `json:"image" yaml:"image"`
	Replicas          int               `json:"replicas" yaml:"replicas"`
	Command           []string          `json:"command,omitempty" yaml:"command,omitempty"`
	Env               map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	Ports             []Port            `json:"ports,omitempty" yaml:"ports,omitempty"`
	Resources         Resources         `json:"resources" yaml:"resources"`
	GPUs              int               `json:"gpus,omitempty" yaml:"gpus,omitempty"`
	GPUModel          string            `json:"gpu_model,omitempty" yaml:"gpu_model,omitempty"`
	GPUMemory         int64             `json:"gpu_memory,omitempty" yaml:"gpu_memory,omitempty"`
	Health            *Health           `json:"health,omitempty" yaml:"health,omitempty"`
	Generation        int64             `json:"generation" yaml:"generation"`
	HealthyGeneration int64             `json:"healthy_generation" yaml:"healthy_generation"`
	SensitiveTo       string            `json:"sensitive_to,omitempty" yaml:"sensitive_to,omitempty"`
	DependsOn         []string          `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	Gang              bool              `json:"gang,omitempty" yaml:"gang,omitempty"`
	NodeLabels        map[string]string `json:"node_labels,omitempty" yaml:"node_labels,omitempty"`
	GangFabric        string            `json:"gang_fabric,omitempty" yaml:"gang_fabric,omitempty"`
	Configs           []string          `json:"configs,omitempty" yaml:"configs,omitempty"`
	Secrets           []string          `json:"secrets,omitempty" yaml:"secrets,omitempty"`
}

type Node struct {
	ID            string            `json:"id" yaml:"id"`
	Addr          string            `json:"addr,omitempty" yaml:"addr,omitempty"`
	Status        string            `json:"status" yaml:"status"`
	Capacity      Resources         `json:"capacity" yaml:"capacity"`
	Free          Resources         `json:"free" yaml:"free"`
	Perf          Perf              `json:"perf" yaml:"perf"`
	Score         float64           `json:"score" yaml:"score"`
	GPUs          int               `json:"gpus" yaml:"gpus"`
	GPUInventory  []GPU             `json:"gpu_inventory,omitempty" yaml:"gpu_inventory,omitempty"`
	Labels        map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	LastSeen      time.Time         `json:"last_seen" yaml:"last_seen"`
	CordonedAt    time.Time         `json:"cordoned_at,omitempty" yaml:"cordoned_at,omitempty"`
	DiskFree      int64             `json:"disk_free" yaml:"disk_free"`
	DiskTotal     int64             `json:"disk_total" yaml:"disk_total"`
	CertNotBefore time.Time         `json:"cert_not_before,omitempty" yaml:"cert_not_before,omitempty"`
	CertNotAfter  time.Time         `json:"cert_not_after,omitempty" yaml:"cert_not_after,omitempty"`
	Epoch         uint64            `json:"epoch" yaml:"epoch"`
}

type Assignment struct {
	ID         string            `json:"id" yaml:"id"`
	App        string            `json:"app" yaml:"app"`
	NodeID     string            `json:"node_id" yaml:"node_id"`
	Image      string            `json:"image" yaml:"image"`
	Generation int64             `json:"generation" yaml:"generation"`
	Epoch      uint64            `json:"epoch" yaml:"epoch"`
	Status     string            `json:"status" yaml:"status"`
	Reason     string            `json:"reason,omitempty" yaml:"reason,omitempty"`
	Restarts   int               `json:"restarts" yaml:"restarts"`
	Replaces   string            `json:"replaces,omitempty" yaml:"replaces,omitempty"`
	RuntimeID  string            `json:"runtime_id,omitempty" yaml:"runtime_id,omitempty"`
	HostPort   int               `json:"host_port,omitempty" yaml:"host_port,omitempty"`
	Logs       string            `json:"logs,omitempty" yaml:"logs,omitempty"`
	Env        map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	Command    []string          `json:"command,omitempty" yaml:"command,omitempty"`
	Ports      []Port            `json:"ports,omitempty" yaml:"ports,omitempty"`
	Resources  Resources         `json:"resources" yaml:"resources"`
	GPUs       int               `json:"gpus,omitempty" yaml:"gpus,omitempty"`
	GPUDevices []string          `json:"gpu_devices,omitempty" yaml:"gpu_devices,omitempty"`
	Kind       string            `json:"kind" yaml:"kind"`
	Updated    time.Time         `json:"updated" yaml:"updated"`
}

type Route struct {
	Kind       string `json:"kind" yaml:"kind"`
	Name       string `json:"name" yaml:"name"`
	App        string `json:"app" yaml:"app"`
	Port       int    `json:"port" yaml:"port"`
	TargetPort int    `json:"target_port,omitempty" yaml:"target_port,omitempty"`
}

type Config struct {
	Kind string            `json:"kind" yaml:"kind"`
	Name string            `json:"name" yaml:"name"`
	Data map[string]string `json:"data" yaml:"data"`
}

type Secret struct {
	Kind string            `json:"kind" yaml:"kind"`
	Name string            `json:"name" yaml:"name"`
	Data map[string]string `json:"data" yaml:"data"`
}

type Policy struct {
	Kind          string   `json:"kind" yaml:"kind"`
	Auto          []string `json:"auto" yaml:"auto"`
	Confirm       []string `json:"confirm" yaml:"confirm"`
	MoveMinGain   float64  `json:"move_min_gain" yaml:"move_min_gain"`
	MoveCooldown  string   `json:"move_cooldown" yaml:"move_cooldown"`
	SoloPromotion string   `json:"solo_promotion" yaml:"solo_promotion"`
	MaxRestarts   int      `json:"max_restarts" yaml:"max_restarts"`
	SuspectAfter  string   `json:"suspect_after,omitempty" yaml:"suspect_after,omitempty"`
	DeadAfter     string   `json:"dead_after,omitempty" yaml:"dead_after,omitempty"`
	LeaseTTL      string   `json:"lease_ttl,omitempty" yaml:"lease_ttl,omitempty"`
}

type Action struct {
	ID     string    `json:"id" yaml:"id"`
	At     time.Time `json:"at" yaml:"at"`
	Actor  string    `json:"actor" yaml:"actor"`
	Kind   string    `json:"kind" yaml:"kind"`
	Target string    `json:"target" yaml:"target"`
	Reason string    `json:"reason" yaml:"reason"`
	Result string    `json:"result" yaml:"result"`
}

type Snapshot struct {
	Index       uint64       `json:"index"`
	Epoch       uint64       `json:"epoch"`
	LeaderID    string       `json:"leader_id"`
	Taken       time.Time    `json:"taken"`
	Apps        []App        `json:"apps"`
	Assignments []Assignment `json:"assignments"`
	Policy      Policy       `json:"policy"`
	Routes      []Route      `json:"routes"`
	Configs     []Config     `json:"configs"`
	Secrets     []Secret     `json:"secrets"`
}

type Lease struct {
	Epoch    uint64    `json:"epoch"`
	LeaderID string    `json:"leader_id"`
	Expires  time.Time `json:"expires"`
	Leading  bool      `json:"leading"`
	URL      string    `json:"url,omitempty"`
}

func (p Policy) Cooldown() time.Duration {
	d, err := time.ParseDuration(p.MoveCooldown)
	if err != nil || d == 0 {
		return 10 * time.Minute
	}
	return d
}

func (p Policy) SoloAfter() time.Duration {
	d, err := time.ParseDuration(p.SoloPromotion)
	if err != nil || d == 0 {
		return 30 * time.Second
	}
	return d
}

func (p Policy) Suspect() time.Duration {
	d, err := time.ParseDuration(p.SuspectAfter)
	if err != nil || d == 0 {
		return 3 * time.Second
	}
	return d
}

func (p Policy) Dead() time.Duration {
	d, err := time.ParseDuration(p.DeadAfter)
	if err != nil || d == 0 {
		return 10 * time.Second
	}
	return d
}

func (p Policy) Lease() time.Duration {
	d, err := time.ParseDuration(p.LeaseTTL)
	if err != nil || d == 0 {
		return 5 * time.Second
	}
	return d
}

func (a App) ReleaseEqual(b App) bool {
	if a.Image != b.Image || a.Kind != b.Kind || a.Gang != b.Gang || a.GangFabric != b.GangFabric || a.GPUs != b.GPUs || a.GPUModel != b.GPUModel || a.GPUMemory != b.GPUMemory || a.SensitiveTo != b.SensitiveTo {
		return false
	}
	if len(a.NodeLabels) != len(b.NodeLabels) {
		return false
	}
	for k, v := range a.NodeLabels {
		if b.NodeLabels[k] != v {
			return false
		}
	}
	if len(a.Command) != len(b.Command) {
		return false
	}
	for i := range a.Command {
		if a.Command[i] != b.Command[i] {
			return false
		}
	}
	if len(a.Env) != len(b.Env) || len(a.Configs) != len(b.Configs) || len(a.Secrets) != len(b.Secrets) {
		return false
	}
	for k, v := range a.Env {
		if b.Env[k] != v {
			return false
		}
	}
	for i := range a.Configs {
		if a.Configs[i] != b.Configs[i] {
			return false
		}
	}
	for i := range a.Secrets {
		if a.Secrets[i] != b.Secrets[i] {
			return false
		}
	}
	return true
}

func Active(status string) bool {
	switch status {
	case StatusStopped, StatusSucceeded:
		return false
	default:
		return true
	}
}
