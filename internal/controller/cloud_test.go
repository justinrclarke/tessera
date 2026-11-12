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

func TestCloudCLICreateRetriesByOwnershipAndRejectsChangedOwnership(t *testing.T) {
	for _, provider := range []string{"aws", "gcp", "azure"} {
		t.Run(provider, func(t *testing.T) {
			key := strings.Repeat("a", 32)
			exists, foreign := false, false
			creates, deletes := 0, 0
			c := CloudCLI{Provider: provider, Settings: cloud.Settings{Project: "test", Subscription: "test", ResourceGroup: "test", AdminUser: "test", SSHKey: "ssh-rsa test"}}
			c.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "private-join-token") {
					t.Fatal("join credential leaked into command arguments")
				}
				owner := key
				if foreign {
					owner = "another-owner"
				}
				if strings.Contains(joined, "describe-instances") || strings.HasPrefix(joined, "compute instances list") || strings.HasPrefix(joined, "vm list") {
					if !exists {
						if provider == "aws" {
							return []byte(`{"Reservations":[]}`), nil
						}
						return []byte(`[]`), nil
					}
					switch provider {
					case "aws":
						return []byte(fmt.Sprintf(`{"Reservations":[{"Instances":[{"InstanceId":"vm","State":{"Name":"running"},"Tags":[{"Key":"Name","Value":"ts-test"},{"Key":"tessera-key","Value":%q}]}]}]}`, owner)), nil
					case "gcp":
						return []byte(fmt.Sprintf(`[{"id":"vm","name":"ts-test","status":"RUNNING","labels":{"tessera-key":%q}}]`, owner)), nil
					case "azure":
						return []byte(fmt.Sprintf(`[{"id":"vm","name":"ts-test","tags":{"tessera-key":%q}}]`, owner)), nil
					}
				}
				if strings.Contains(joined, "run-instances") || strings.HasPrefix(joined, "compute instances create") || strings.HasPrefix(joined, "vm create") {
					creates++
					exists = true
					if provider == "aws" {
						for i, arg := range args {
							if arg == "--cli-input-json" {
								body, err := os.ReadFile(strings.TrimPrefix(args[i+1], "file://"))
								if err != nil {
									t.Fatal(err)
								}
								var in map[string]any
								if err := json.Unmarshal(body, &in); err != nil || in["ClientToken"] != key {
									t.Fatalf("unstable create request: %s %v", body, err)
								}
							}
						}
						return []byte(`{"Instances":[{"InstanceId":"vm"}]}`), nil
					}
					return []byte(`{}`), nil
				}
				if strings.Contains(joined, "terminate-instances") || strings.Contains(joined, "instances delete") || strings.HasPrefix(joined, "vm delete") {
					deletes++
					return []byte(`{}`), nil
				}
				return nil, fmt.Errorf("unexpected %s %v", name, args)
			}
			spec := cloud.MachineSpec{Name: "ts-test", Type: "small", Region: "test-region", Image: "image", Key: key, UserData: "private-join-token"}
			first, err := c.Create(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			second, err := c.Create(context.Background(), spec)
			if err != nil || first != second || creates != 1 {
				t.Fatalf("duplicate create: %+v %+v creates=%d err=%v", first, second, creates, err)
			}
			foreign = true
			if err := c.Stop(context.Background(), first); err == nil || deletes != 0 {
				t.Fatal("changed ownership was ignored")
			}
			foreign = false
			if err := c.Stop(context.Background(), first); err != nil || deletes != 1 {
				t.Fatalf("owned stop failed: %v", err)
			}
		})
	}
}
