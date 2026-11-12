package cloud

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("cloud resource not found")

type Settings struct {
	Region        string `json:"region,omitempty" yaml:"region,omitempty"`
	Project       string `json:"project,omitempty" yaml:"project,omitempty"`
	Subscription  string `json:"subscription,omitempty" yaml:"subscription,omitempty"`
	ResourceGroup string `json:"resource_group,omitempty" yaml:"resource_group,omitempty"`
	SSHKey        string `json:"ssh_key,omitempty" yaml:"ssh_key,omitempty"`
	AdminUser     string `json:"admin_user,omitempty" yaml:"admin_user,omitempty"`
}

type Resource struct {
	ID     string `json:"id"`
	Subnet string `json:"subnet,omitempty"`
	Key    string `json:"key"`
	Name   string `json:"name"`
	Region string `json:"region"`
}

type NetworkSpec struct{ Name, Region, CIDR, Key string }
type MachineSpec struct{ Name, Region, Image, Type, Key, Network, Subnet, UserData string }

type Provider interface {
	Discover(context.Context) ([]Instance, error)
	CreateNetwork(context.Context, NetworkSpec) (Resource, error)
	Create(context.Context, MachineSpec) (Resource, error)
	Inspect(context.Context, Resource) (Instance, error)
	InspectNetwork(context.Context, Resource) error
	Stop(context.Context, Resource) error
	DeleteNetwork(context.Context, Resource) error
}
