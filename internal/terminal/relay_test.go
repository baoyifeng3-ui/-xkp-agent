package terminal

import (
	"strings"
	"testing"
	"time"
)

func TestRelayURLRequiresWSSOutsideDevelopment(t *testing.T) {
	if err := validateRelayURL("https://management.example/terminal/v1/agent", "session", false); err == nil {
		t.Fatal("expected non-WSS relay URL to be rejected")
	}
}

func TestRelayURLAllowsOnlyLoopbackWSInDevelopment(t *testing.T) {
	if err := validateRelayURL("ws://127.0.0.1:8080/terminal/v1/agent/abc", "abc", true); err != nil {
		t.Fatal(err)
	}
	if err := validateRelayURL("ws://management.example/terminal/v1/agent", "abc", true); err == nil {
		t.Fatal("expected non-loopback development relay URL to be rejected")
	}
}

func TestRelayURLIsAlreadyCompleteAndReturnedUnchanged(t *testing.T) {
	raw := "wss://management.example/terminal/v1/agent/abc"
	if err := validateRelayURL(raw, "abc", false); err != nil {
		t.Fatal(err)
	}
	got, err := relayPath(raw, "abc")
	if err != nil || got != raw {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{
		"wss://management.example/terminal/v1/agent",
		"wss://management.example/terminal/v1/agent/abc?ticket=secret",
		"wss://management.example/terminal/v1/agent/%61bc",
		"wss://user@management.example/terminal/v1/agent/abc",
	} {
		if validateRelayURL(bad, "abc", false) == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestParseControlIsStrict(t *testing.T) {
	valid := []string{`{"type":"resize","columns":80,"rows":24}`, `{"type":"ping"}`, `{"type":"pong"}`, `{"type":"close","reason":"browser-disconnected"}`, `{"type":"close","reason":"operator-request"}`}
	for _, raw := range valid {
		if _, err := parseControl([]byte(raw)); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	invalid := []string{`{"type":"resize","cols":80,"rows":24}`, `{"type":"ping","extra":1}`, `{"type":"ping"} {}`, `{"type":"ping","type":"pong"}`, `{"type":"close","reason":"arbitrary"}`, strings.Repeat("x", maxControlFrame+1)}
	for _, raw := range invalid {
		if _, err := parseControl([]byte(raw)); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestTokenBucketFailsClosedWithoutSleeping(t *testing.T) {
	now := time.Unix(100, 0)
	b := newTokenBucket(now)
	if !b.allow(tokenBurst, now) {
		t.Fatal("initial burst denied")
	}
	if b.allow(1, now) {
		t.Fatal("over burst allowed")
	}
	if !b.allow(tokenRate, now.Add(time.Second)) {
		t.Fatal("refill denied")
	}
}
