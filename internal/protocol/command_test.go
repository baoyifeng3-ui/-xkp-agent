package protocol

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDecodeCommandAcceptsSharedShutdownFixture(t *testing.T) {
	data, err := os.ReadFile("../../testdata/command-shutdown-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	command, err := DecodeCommand(data)
	if err != nil {
		t.Fatal(err)
	}
	if command.CommandID != "11111111-2222-4333-8444-555555555555" || command.Type != ShutdownServer || command.Version != 1 {
		t.Fatalf("command = %#v", command)
	}
	if string(command.Payload) != "{}" {
		t.Fatalf("payload = %s", command.Payload)
	}
}

func TestDecodeCommandRejectsUnknownFields(t *testing.T) {
	_, err := DecodeCommand([]byte(`{"commandId":"11111111-2222-4333-8444-555555555555","type":"SHUTDOWN_SERVER","version":1,"leaseToken":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","leaseExpiresAt":"2026-08-19T12:05:00Z","payload":{},"shell":"poweroff"}`))
	if err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestDecodeCommandRejectsTrailingJSON(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/command-shutdown-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodeCommand(append(fixture, []byte(` {}`)...))
	if err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
}

func TestDecodeCommandRejectsInvalidEnvelope(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"command id", `{"commandId":"bad","type":"SHUTDOWN_SERVER","version":1,"leaseToken":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","leaseExpiresAt":"2026-08-19T12:05:00Z","payload":{}}`},
		{"lease token", `{"commandId":"11111111-2222-4333-8444-555555555555","type":"SHUTDOWN_SERVER","version":1,"leaseToken":"bad","leaseExpiresAt":"2026-08-19T12:05:00Z","payload":{}}`},
		{"version", `{"commandId":"11111111-2222-4333-8444-555555555555","type":"SHUTDOWN_SERVER","version":2,"leaseToken":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","leaseExpiresAt":"2026-08-19T12:05:00Z","payload":{}}`},
		{"type", `{"commandId":"11111111-2222-4333-8444-555555555555","type":"RUN_SHELL","version":1,"leaseToken":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","leaseExpiresAt":"2026-08-19T12:05:00Z","payload":{}}`},
		{"payload", `{"commandId":"11111111-2222-4333-8444-555555555555","type":"SHUTDOWN_SERVER","version":1,"leaseToken":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","leaseExpiresAt":"2026-08-19T12:05:00Z","payload":{"force":true}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeCommand([]byte(test.body)); err == nil {
				t.Fatal("expected invalid envelope rejection")
			}
		})
	}
}

func TestDecodeCommandRejectsOversizedInput(t *testing.T) {
	_, err := DecodeCommand([]byte(strings.Repeat(" ", MaxCommandBytes+1)))
	if err == nil {
		t.Fatal("expected oversized command rejection")
	}
}

func TestDecodeCommandAcceptsSharedEnvironmentFixtures(t *testing.T) {
	tests := []struct {
		name     string
		typeName CommandType
		full     bool
	}{
		{"create", CreateTrainingEnvironment, true},
		{"restore", RestoreTrainingEnvironment, true},
		{"start", StartTrainingEnvironment, false},
		{"stop", StopTrainingEnvironment, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := os.ReadFile("../../testdata/command-" + test.name + "-training-environment-v1.json")
			if err != nil {
				t.Fatal(err)
			}
			command, err := DecodeCommand(data)
			if err != nil {
				t.Fatal(err)
			}
			if command.Type != test.typeName || command.Environment == nil {
				t.Fatalf("command = %#v", command)
			}
			if len(command.Environment.Components) != 2 {
				t.Fatalf("components = %d", len(command.Environment.Components))
			}
			if test.full && command.Environment.WorkspaceRelativePath != "training/7/101" {
				t.Fatalf("workspace = %q", command.Environment.WorkspaceRelativePath)
			}
		})
	}
}

func TestDecodeCommandRejectsUnsafeEnvironmentPayloads(t *testing.T) {
	data, err := os.ReadFile("../../testdata/command-create-training-environment-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	payload := envelope["payload"].(map[string]interface{})
	components := payload["components"].([]interface{})

	mutations := []struct {
		name   string
		change func()
	}{
		{"absolute workspace", func() { payload["workspaceRelativePath"] = "/etc" }},
		{"traversing workspace", func() { payload["workspaceRelativePath"] = "training/../etc" }},
		{"unknown runtime", func() { components[0].(map[string]interface{})["runtimeName"] = "runc" }},
		{"unknown mount", func() { components[0].(map[string]interface{})["mountTarget"] = "/etc" }},
		{"duplicate host port", func() {
			components[1].(map[string]interface{})["ports"].([]interface{})[0].(map[string]interface{})["hostPort"] = float64(8081)
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			var copyEnvelope map[string]interface{}
			encoded, _ := json.Marshal(envelope)
			_ = json.Unmarshal(encoded, &copyEnvelope)
			copyPayload := copyEnvelope["payload"].(map[string]interface{})
			copyComponents := copyPayload["components"].([]interface{})
			payload, components = copyPayload, copyComponents
			mutation.change()
			invalid, _ := json.Marshal(copyEnvelope)
			if _, err := DecodeCommand(invalid); err == nil {
				t.Fatal("expected unsafe payload rejection")
			}
		})
	}
}
