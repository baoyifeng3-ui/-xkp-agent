package protocol

import (
	"fmt"
	"testing"
	"time"
)

func TestDecodeDockerInventory(t *testing.T) {
	for _, tc := range []struct {
		payload string
		valid   bool
	}{
		{`{"action":"INSPECT_IMAGE","target":"registry.example.com:5000/repo:latest"}`, true},
		{`{"action":"DELETE_CONTAINER","target":"container-1"}`, true},
		{`{"action":"DELETE_IMAGE","target":"--force"}`, false},
		{`{"action":"DELETE_IMAGE","target":"repo:latest","force":true}`, false},
		{`{"action":"PRUNE","target":"repo:latest"}`, false},
	} {
		data := fmt.Sprintf(`{"commandId":"11111111-2222-4333-8444-555555555555","type":"DOCKER_INVENTORY_ACTION","version":1,"leaseToken":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","leaseExpiresAt":"2026-09-09T12:05:00Z","payload":%s}`, tc.payload)
		command, err := DecodeCommandAt([]byte(data), time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC), false)
		if (err == nil) != tc.valid || (tc.valid && command.DockerInventory == nil) {
			t.Fatalf("%s: %v", tc.payload, err)
		}
	}
}
