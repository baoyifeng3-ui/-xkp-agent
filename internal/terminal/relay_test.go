package terminal

import (
	"testing"
)

func TestRelayURLRequiresWSSOutsideDevelopment(t *testing.T) {
	if err := validateRelayURL("https://management.example/terminal/v1/agent", "session", false); err == nil {
		t.Fatal("expected non-WSS relay URL to be rejected")
	}
}

func TestRelayURLAllowsOnlyLoopbackWSInDevelopment(t *testing.T) {
	if err := validateRelayURL("ws://127.0.0.1:8080/terminal/v1/agent", "abc", true); err != nil {
		t.Fatal(err)
	}
	if err := validateRelayURL("ws://management.example/terminal/v1/agent", "abc", true); err == nil {
		t.Fatal("expected non-loopback development relay URL to be rejected")
	}
}
