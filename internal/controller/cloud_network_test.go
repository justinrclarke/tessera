package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"tessera/internal/cloud"
)

func TestAWSNetworkResumesStackAfterLostCreateResponseAndChecksIdentity(t *testing.T) {
	ctx := context.Background()
	created, lost := false, false
	creates, deletes := 0, 0
	key := strings.Repeat("a", 32)
	c := CloudCLI{Provider: "aws", Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[1] {
		case "describe-stacks":
			if !created {
				return []byte(`{"Stacks":[]}`), nil
			}
			return []byte(fmt.Sprintf(`{"Stacks":[{"StackId":"stack-id","StackName":"ts-network","StackStatus":"CREATE_COMPLETE","Tags":[{"Key":"tessera-key","Value":%q}],"Outputs":[{"OutputKey":"Network","OutputValue":"vpc-id"},{"OutputKey":"Subnet","OutputValue":"subnet-id"}]}]}`, key)), nil
		case "create-stack":
			creates++
			created = true
			for i, arg := range args {
				if arg == "--template-body" {
					body, err := os.ReadFile(strings.TrimPrefix(args[i+1], "file://"))
					if err != nil {
						t.Fatal(err)
					}
					var template map[string]any
					if json.Unmarshal(body, &template) != nil || len(template["Resources"].(map[string]any)) != 2 {
						t.Fatal("network and subnet are not one recoverable provider operation")
					}
				}
			}
			if !lost {
				lost = true
				return nil, fmt.Errorf("response lost")
			}
		case "delete-stack":
			deletes++
		case "wait":
		default:
			t.Fatalf("unexpected command: %v", args)
		}
		return []byte(`{}`), nil
	}}
	spec := cloud.NetworkSpec{Name: "ts-network", Region: "us-east-1", CIDR: "10.0.0.0/24", Key: key}
	if _, err := c.CreateNetwork(ctx, spec); err == nil {
		t.Fatal("lost response was hidden")
	}
	r, err := c.CreateNetwork(ctx, spec)
	if err != nil || r.ID != "vpc-id" || r.Subnet != "subnet-id" || creates != 1 {
		t.Fatalf("stack retry duplicated network: %+v %d %v", r, creates, err)
	}
	changed := r
	changed.ID = "another-vpc"
	if err := c.DeleteNetwork(ctx, changed); err == nil || deletes != 0 {
		t.Fatal("deleted a changed network identity")
	}
	if err := c.DeleteNetwork(ctx, r); err != nil || deletes != 1 {
		t.Fatalf("owned network removal: %v", err)
	}
}

func TestGCPNetworkDeletionRetriesAfterSubnetWasAlreadyDeleted(t *testing.T) {
	ctx := context.Background()
	key := strings.Repeat("b", 32)
	network, subnet, lose := true, true, true
	subnetDeletes, networkDeletes := 0, 0
	c := CloudCLI{Provider: "gcp", Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		command := strings.Join(args[:min(len(args), 4)], " ")
		switch command {
		case "compute networks list --format=json":
			if !network {
				return []byte(`[]`), nil
			}
			return []byte(fmt.Sprintf(`[{"name":"ts-network","description":"tessera:%s"}]`, key)), nil
		case "compute networks subnets list":
			if !subnet {
				return []byte(`[]`), nil
			}
			return []byte(fmt.Sprintf(`[{"name":"ts-network","description":"tessera:%s","region":"https://example.test/regions/us-central1"}]`, key)), nil
		case "compute networks subnets delete":
			subnetDeletes++
			subnet = false
			if lose {
				lose = false
				return nil, fmt.Errorf("lost delete response")
			}
		case "compute networks delete ts-network":
			networkDeletes++
			network = false
		default:
			t.Fatalf("unexpected command: %v", args)
		}
		return []byte(`{}`), nil
	}}
	r := cloud.Resource{ID: "ts-network", Name: "ts-network", Subnet: "ts-network", Key: key, Region: "us-central1-a"}
	if err := c.DeleteNetwork(ctx, r); err == nil {
		t.Fatal("lost subnet deletion was hidden")
	}
	for range 2 {
		if err := c.DeleteNetwork(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if subnetDeletes != 1 || networkDeletes != 1 {
		t.Fatalf("retry repeated deletion: subnets=%d networks=%d", subnetDeletes, networkDeletes)
	}
}
