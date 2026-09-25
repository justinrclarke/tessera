package cloud

import (
	"context"
	"strings"
	"testing"
)

func TestCloudInventoriesUseConfiguredCLIs(t *testing.T) {
	tests := []struct {
		config Inventory
		bin    string
		args   string
		body   string
		want   Instance
	}{
		{
			config: Inventory{Provider: "aws", Region: "us-east-1"},
			bin:    "aws", args: "ec2 describe-instances --output json --region us-east-1",
			body: `{"Reservations":[{"Instances":[{"InstanceId":"i-1","InstanceType":"g5.xlarge","State":{"Name":"running"},"Placement":{"AvailabilityZone":"us-east-1a"},"PrivateIpAddress":"10.0.0.1","Tags":[{"Key":"Name","Value":"gpu-node"}]},{"InstanceId":"i-old","State":{"Name":"terminated"}}]}]}`,
			want: Instance{Provider: "aws", ID: "i-1", Name: "gpu-node", Region: "us-east-1a", Type: "g5.xlarge", State: "running", Private: "10.0.0.1"},
		},
		{
			config: Inventory{Provider: "gcp", Project: "demo"},
			bin:    "gcloud", args: "compute instances list --format=json --project demo",
			body: `[{"id":"123","name":"gpu-node","zone":"https://compute.googleapis.com/compute/v1/projects/demo/zones/us-central1-a","machineType":"zones/us-central1-a/machineTypes/g2-standard-4","status":"RUNNING","networkInterfaces":[{"networkIP":"10.0.0.2","accessConfigs":[{"natIP":"1.2.3.4"}]}]}]`,
			want: Instance{Provider: "gcp", ID: "123", Name: "gpu-node", Region: "us-central1-a", Type: "g2-standard-4", State: "running", Private: "10.0.0.2", Public: "1.2.3.4"},
		},
		{
			config: Inventory{Provider: "azure", Subscription: "sub-1"},
			bin:    "az", args: "vm list --show-details --output json --subscription sub-1",
			body: `[{"id":"/subscriptions/sub-1/resourceGroups/test/providers/Microsoft.Compute/virtualMachines/gpu-node","name":"gpu-node","location":"eastus","hardwareProfile":{"vmSize":"Standard_NC4"},"powerState":"VM running","privateIps":"10.0.0.3"}]`,
			want: Instance{Provider: "azure", ID: "/subscriptions/sub-1/resourceGroups/test/providers/Microsoft.Compute/virtualMachines/gpu-node", Name: "gpu-node", Region: "eastus", Type: "Standard_NC4", State: "VM running", Private: "10.0.0.3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.config.Provider, func(t *testing.T) {
			inv := tt.config
			inv.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != tt.bin || strings.Join(args, " ") != tt.args {
					t.Fatalf("command %s %v", name, args)
				}
				return []byte(tt.body), nil
			}
			items, err := inv.List(context.Background())
			if err != nil || len(items) != 1 || items[0] != tt.want {
				t.Fatalf("inventory %+v: %v", items, err)
			}
		})
	}
}

func TestUnknownCloudProviderDoesNotRunCommand(t *testing.T) {
	inv := Inventory{Provider: "unknown", Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("ran command for unknown provider")
		return nil, nil
	}}
	if _, err := inv.List(context.Background()); err == nil {
		t.Fatal("accepted unknown provider")
	}
}
