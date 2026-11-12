package controller

import (
	"context"
	"encoding/json"
	"fmt"

	"tessera/internal/cloud"
)

type awsStack struct {
	ID      string                        `json:"StackId"`
	Name    string                        `json:"StackName"`
	Status  string                        `json:"StackStatus"`
	Tags    []struct{ Key, Value string } `json:"Tags"`
	Outputs []struct {
		Key   string `json:"OutputKey"`
		Value string `json:"OutputValue"`
	} `json:"Outputs"`
}

func (c CloudCLI) ownedStack(ctx context.Context, name, region, key string) (*awsStack, error) {
	var out struct {
		Stacks []awsStack `json:"Stacks"`
	}
	if err := c.run(ctx, region, &out, "cloudformation", "describe-stacks"); err != nil {
		return nil, err
	}
	for _, stack := range out.Stacks {
		if stack.Name != name {
			continue
		}
		owned := false
		for _, tag := range stack.Tags {
			owned = owned || tag.Key == "tessera-key" && tag.Value == key
		}
		if !owned {
			return nil, fmt.Errorf("stack name belongs to another owner")
		}
		return &stack, nil
	}
	return nil, nil
}

func (c CloudCLI) createAWSNetwork(ctx context.Context, spec cloud.NetworkSpec) (cloud.Resource, error) {
	r := cloud.Resource{Name: spec.Name, Region: spec.Region, Key: spec.Key}
	stack, err := c.ownedStack(ctx, spec.Name, spec.Region, spec.Key)
	if err != nil {
		return r, err
	}
	if stack == nil {
		tags := []any{map[string]string{"Key": "tessera-key", "Value": spec.Key}}
		template := map[string]any{"AWSTemplateFormatVersion": "2010-09-09", "Resources": map[string]any{"Network": map[string]any{"Type": "AWS::EC2::VPC", "Properties": map[string]any{"CidrBlock": spec.CIDR, "EnableDnsSupport": true, "EnableDnsHostnames": true, "Tags": tags}}, "Subnet": map[string]any{"Type": "AWS::EC2::Subnet", "Properties": map[string]any{"VpcId": map[string]string{"Ref": "Network"}, "CidrBlock": spec.CIDR, "Tags": tags}}}, "Outputs": map[string]any{"Network": map[string]any{"Value": map[string]string{"Ref": "Network"}}, "Subnet": map[string]any{"Value": map[string]string{"Ref": "Subnet"}}}}
		body, _ := json.Marshal(template)
		file, cleanup, err := privateCloudFile(body)
		if err != nil {
			return r, err
		}
		defer cleanup()
		if err := c.run(ctx, spec.Region, nil, "cloudformation", "create-stack", "--stack-name", spec.Name, "--client-request-token", spec.Key, "--template-body", "file://"+file, "--tags", "Key=tessera-key,Value="+spec.Key); err != nil {
			return r, err
		}
	}
	if err := c.run(ctx, spec.Region, nil, "cloudformation", "wait", "stack-create-complete", "--stack-name", spec.Name); err != nil {
		return r, err
	}
	stack, err = c.ownedStack(ctx, spec.Name, spec.Region, spec.Key)
	if err != nil {
		return r, err
	}
	if stack == nil || stack.Status != "CREATE_COMPLETE" {
		return r, fmt.Errorf("network stack is not ready")
	}
	for _, output := range stack.Outputs {
		switch output.Key {
		case "Network":
			r.ID = output.Value
		case "Subnet":
			r.Subnet = output.Value
		}
	}
	if r.ID == "" || r.Subnet == "" {
		return r, fmt.Errorf("network stack has incomplete outputs")
	}
	return r, nil
}

func (c CloudCLI) deleteAWSNetwork(ctx context.Context, r cloud.Resource) error {
	stack, err := c.ownedStack(ctx, r.Name, r.Region, r.Key)
	if err != nil {
		return err
	}
	if stack == nil {
		return nil
	}
	matched := false
	for _, output := range stack.Outputs {
		if output.Key == "Network" {
			matched = output.Value == r.ID
		}
	}
	if !matched {
		return fmt.Errorf("network stack identity changed")
	}
	if err := c.run(ctx, r.Region, nil, "cloudformation", "delete-stack", "--stack-name", stack.ID, "--client-request-token", "delete-"+r.Key); err != nil {
		return err
	}
	return c.run(ctx, r.Region, nil, "cloudformation", "wait", "stack-delete-complete", "--stack-name", stack.ID)
}
