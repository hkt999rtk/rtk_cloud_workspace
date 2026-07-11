package main

import (
	"reflect"
	"testing"
)

func TestRedactCommandArgs(t *testing.T) {
	got := redactCommandArgs([]string{
		"env",
		"VIDEO_CLOUD_COTURN_SHARED_SECRET=secret-value",
		"VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY=key-value",
		"LINODE_TOKEN=token-value",
		"SAFE=value",
	})
	want := []string{
		"env",
		"VIDEO_CLOUD_COTURN_SHARED_SECRET=<redacted>",
		"VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY=<redacted>",
		"LINODE_TOKEN=<redacted>",
		"SAFE=value",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("redactCommandArgs() = %#v, want %#v", got, want)
	}
}
