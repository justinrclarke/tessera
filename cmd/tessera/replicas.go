package main

import (
	"fmt"
	"strings"

	"tessera/internal/controller"
)

func parseControllerPeers(value string) ([]controller.ControllerPeer, error) {
	var peers []controller.ControllerPeer
	for _, item := range strings.Split(value, ",") {
		id, rest, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok || id == "" {
			return nil, fmt.Errorf("peers must use ID=RAFT_ADDRESS@HTTP_URL")
		}
		address, url, ok := strings.Cut(rest, "@")
		if !ok || address == "" || url == "" {
			return nil, fmt.Errorf("peers must use ID=RAFT_ADDRESS@HTTP_URL")
		}
		peers = append(peers, controller.ControllerPeer{ID: id, Address: address, URL: strings.TrimRight(url, "/")})
	}
	return peers, nil
}
