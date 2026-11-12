package controller

import (
	"context"
	"errors"
	"fmt"

	"tessera/internal/cloud"
	"tessera/internal/store"
)

func (s *Server) discoverInfrastructure(ctx context.Context, state InfraState) error {
	missingMachines, missingNetworks := map[string]bool{}, map[string]bool{}
	for _, machine := range state.State.Machines {
		settings := state.Desired.Providers[machine.Machine.Provider]
		settings.Region = machine.Machine.Region
		_, err := s.provider(machine.Machine.Provider, settings).Inspect(ctx, machine.Resource)
		if errors.Is(err, cloud.ErrNotFound) && machine.Owned {
			missingMachines[machine.Machine.Name] = true
		} else if err != nil {
			return fmt.Errorf("inspect machine %s: %w", machine.Machine.Name, err)
		}
	}
	for _, network := range state.State.Networks {
		settings := state.Desired.Providers[network.Network.Provider]
		settings.Region = network.Network.Region
		err := s.provider(network.Network.Provider, settings).InspectNetwork(ctx, network.Resource)
		if errors.Is(err, cloud.ErrNotFound) && network.Owned {
			missingNetworks[network.Network.Name] = true
		} else if err != nil {
			return fmt.Errorf("inspect network %s: %w", network.Network.Name, err)
		}
	}
	return s.commitInfra(ctx, state.Revision, func(_ *store.Store, current *InfraState) error {
		if current.Attempts == nil {
			current.Attempts = map[string]int{}
		}
		machines := current.State.Machines[:0]
		for _, m := range current.State.Machines {
			if missingMachines[m.Machine.Name] {
				current.Attempts["machine:"+m.Machine.Name]++
				current.Phase = "pending"
			} else {
				machines = append(machines, m)
			}
		}
		current.State.Machines = machines
		networks := current.State.Networks[:0]
		for _, n := range current.State.Networks {
			if missingNetworks[n.Network.Name] {
				current.Attempts["network:"+n.Network.Name]++
				current.Phase = "pending"
			} else {
				networks = append(networks, n)
			}
		}
		current.State.Networks = networks
		current.DiscoveredAt = s.now()
		return nil
	})
}
