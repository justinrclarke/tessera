package main

import (
	"reflect"
	"testing"

	"tessera/internal/controller"
)

func TestControllerPeerFlags(t *testing.T) {
	peers, err := parseControllerPeers("a=host-a:7469@http://host-a:7468/, b=host-b:7469@http://host-b:7468,c=[::1]:7469@http://[::1]:7468")
	want := []controller.ControllerPeer{
		{ID: "a", Address: "host-a:7469", URL: "http://host-a:7468"},
		{ID: "b", Address: "host-b:7469", URL: "http://host-b:7468"},
		{ID: "c", Address: "[::1]:7469", URL: "http://[::1]:7468"},
	}
	if err != nil || !reflect.DeepEqual(peers, want) {
		t.Fatalf("parsed peers %+v: %v", peers, err)
	}
	for _, value := range []string{"", "a", "=host@http://host", "a=@http://host", "a=host@"} {
		if _, err := parseControllerPeers(value); err == nil {
			t.Fatalf("accepted malformed peers %q", value)
		}
	}
}
