package protocol

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const rootTerminalFixture = "../../testdata/command-open-root-terminal-v1.json"

var rootTerminalNow = time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

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

func TestDecodeCommandPreservesLegacyIdentifierAndTimestampCompatibility(t *testing.T) {
	shutdown := []byte(`{"commandId":"11111111-2222-4333-8444-555555555555","type":"SHUTDOWN_SERVER","version":1,"leaseToken":"AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE","leaseExpiresAt":"2026-08-19T12:05:00.123+00:00","payload":{}}`)
	if _, err := DecodeCommand(shutdown); err != nil {
		t.Fatalf("legacy shutdown rejected: %v", err)
	}
	environment, err := os.ReadFile("../../testdata/command-start-training-environment-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal(environment, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["commandId"] = "11111111-2222-4333-8444-555555555555"
	envelope["leaseToken"] = "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
	envelope["leaseExpiresAt"] = "2026-08-19T12:05:00.123+00:00"
	payload := envelope["payload"].(map[string]interface{})
	payload["environmentId"] = "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
	payload["operationId"] = "BBBBBBBB-CCCC-4DDD-8EEE-FFFFFFFFFFFF"
	mutated, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCommand(mutated); err != nil {
		t.Fatalf("legacy environment rejected: %v", err)
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

func TestDecodeCommandAcceptsModelWorkspaceActions(t *testing.T) {
	for _, payload := range []string{
		`{"action":"LIST","containerName":"xkp-train-editor","modelPath":"","configPath":"","overwrite":false}`,
		`{"action":"DEPLOY","containerName":"xkp-train-editor","modelPath":"models/a.onnx","configPath":"models/a.json","overwrite":true}`,
	} {
		body := []byte(`{"commandId":"11111111-2222-4333-8444-555555555555","type":"MODEL_WORKSPACE","version":1,"leaseToken":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","leaseExpiresAt":"2026-08-19T12:05:00Z","payload":` + payload + `}`)
		command, err := DecodeCommand(body)
		if err != nil || command.ModelWorkspace == nil {
			t.Fatalf("payload=%s command=%#v error=%v", payload, command, err)
		}
	}
}

func TestDecodeCommandRejectsUnsafeModelWorkspacePaths(t *testing.T) {
	body := []byte(`{"commandId":"11111111-2222-4333-8444-555555555555","type":"MODEL_WORKSPACE","version":1,"leaseToken":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","leaseExpiresAt":"2026-08-19T12:05:00Z","payload":{"action":"DEPLOY","containerName":"xkp-train-editor","modelPath":"../secret","configPath":"a.json","overwrite":false}}`)
	if _, err := DecodeCommand(body); err == nil {
		t.Fatal("unsafe model path was accepted")
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

func TestDecodeCreateEnvironmentAcceptsUnlimitedCPUAndMemory(t *testing.T) {
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
	component := components[0].(map[string]interface{})
	component["cpuLimitMillis"] = float64(0)
	component["memoryLimitBytes"] = float64(0)
	mutated, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCommand(mutated); err != nil {
		t.Fatalf("unlimited component rejected: %v", err)
	}
}

func TestDecodeCommandAcceptsSharedCompetitionEnvironmentFixtures(t *testing.T) {
	tests := []struct {
		name     string
		typeName CommandType
		full     bool
	}{
		{"create", CreateCompetitionEnvironment, true},
		{"restore", RestoreCompetitionEnvironment, true},
		{"start", StartCompetitionEnvironment, false},
		{"stop", StopCompetitionEnvironment, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := os.ReadFile("../../testdata/command-" + test.name + "-competition-environment-v1.json")
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

func TestDecodeCompetitionFixtureRetainsEnvelopeSafetyRules(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/command-create-competition-environment-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	valid := string(fixture)
	tests := []struct {
		name string
		body []byte
	}{
		{"unknown field", []byte(strings.Replace(valid, `"version":1`, `"version":1,"shell":"poweroff"`, 1))},
		{"duplicate field", []byte(strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1))},
		{"trailing JSON", append(fixture, []byte(` {}`)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeCommand(test.body); err == nil {
				t.Fatal("expected competition envelope rejection")
			}
		})
	}
	if _, err := DecodeCommand(append(fixture, []byte(strings.Repeat(" ", MaxCommandBytes))...)); err == nil {
		t.Fatal("expected oversized competition envelope rejection")
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

func TestDecodeCommandAtAcceptsSharedRootTerminalFixture(t *testing.T) {
	data, err := os.ReadFile(rootTerminalFixture)
	if err != nil {
		t.Fatal(err)
	}
	command, err := DecodeCommandAt(data, rootTerminalNow, false)
	if err != nil {
		t.Fatal(err)
	}
	if command.CommandID != "77777777-7777-4777-8777-777777777777" ||
		command.Type != OpenRootTerminal || command.Version != 1 ||
		command.LeaseToken != "66666666-6666-4666-8666-666666666666" {
		t.Fatalf("command = %#v", command)
	}
	if command.LeaseExpiresAt != time.Date(2026, 8, 19, 12, 5, 0, 0, time.UTC) {
		t.Fatalf("lease expiry = %s", command.LeaseExpiresAt)
	}
	if command.Terminal == nil || command.Environment != nil {
		t.Fatalf("typed payloads = terminal %#v environment %#v", command.Terminal, command.Environment)
	}
	payload := command.Terminal
	if payload.SessionID != "44444444-4444-4444-8444-444444444444" ||
		payload.RelayURL != "wss://management.example/terminal/v1/agent/44444444-4444-4444-8444-444444444444" ||
		payload.AgentConnectionDeadline != time.Date(2026, 8, 19, 12, 1, 30, 0, time.UTC) ||
		payload.IdleTimeoutSeconds != 600 ||
		payload.AbsoluteExpiresAt != time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC) {
		t.Fatalf("terminal payload = %#v", payload)
	}
}

func TestDecodeCommandRejectsExpiredRootTerminalAtRuntime(t *testing.T) {
	data, err := os.ReadFile(rootTerminalFixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCommand(data); err == nil {
		t.Fatal("expected historical connection deadline rejection")
	}
}

func TestDecodeCommandAtRejectsUnsafeRootTerminalPayloads(t *testing.T) {
	data, err := os.ReadFile(rootTerminalFixture)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{"unknown ticket", `"idleTimeoutSeconds": 600`, `"ticket": "secret", "idleTimeoutSeconds": 600`},
		{"unknown credential", `"idleTimeoutSeconds": 600`, `"credential": "secret", "idleTimeoutSeconds": 600`},
		{"unknown shell", `"idleTimeoutSeconds": 600`, `"shell": "powershell", "idleTimeoutSeconds": 600`},
		{"unknown executable", `"idleTimeoutSeconds": 600`, `"executable": "cmd.exe", "idleTimeoutSeconds": 600`},
		{"unknown environment", `"idleTimeoutSeconds": 600`, `"environment": {}, "idleTimeoutSeconds": 600`},
		{"unknown command", `"idleTimeoutSeconds": 600`, `"command": "whoami", "idleTimeoutSeconds": 600`},
		{"insecure ws", "wss://management.example/", "ws://management.example/"},
		{"http scheme", "wss://management.example/", "http://management.example/"},
		{"file scheme", "wss://management.example/", "file://management.example/"},
		{"userinfo", "wss://management.example/", "wss://user:secret@management.example/"},
		{"missing host", "wss://management.example/", "wss:///"},
		{"query", `wss://management.example/terminal/v1/agent/44444444-4444-4444-8444-444444444444`, `wss://management.example/terminal/v1/agent/44444444-4444-4444-8444-444444444444?credential=secret`},
		{"fragment", `wss://management.example/terminal/v1/agent/44444444-4444-4444-8444-444444444444`, `wss://management.example/terminal/v1/agent/44444444-4444-4444-8444-444444444444#fragment`},
		{"wrong relay session", "/44444444-4444-4444-8444-444444444444", "/55555555-5555-4555-8555-555555555555"},
		{"wrong relay path", "/terminal/v1/agent/", "/terminal/v1/browser/"},
		{"encoded relay path", "/terminal/v1/agent/", "/terminal%2Fv1/agent/"},
		{"noncanonical session", "44444444-4444-4444-8444-444444444444", "44444444-4444-4444-8444-44444444444A"},
		{"noncanonical command id", "77777777-7777-4777-8777-777777777777", "77777777-7777-4777-8777-77777777777A"},
		{"idle timeout", `"idleTimeoutSeconds": 600`, `"idleTimeoutSeconds": 599`},
		{"deadline not future", "2026-08-19T12:01:30Z", "2026-08-19T12:00:00Z"},
		{"absolute not after deadline", "2026-08-19T14:00:00Z", "2026-08-19T12:01:30Z"},
		{"absolute beyond two hours", "2026-08-19T14:00:00Z", "2026-08-19T14:01:31Z"},
		{"non UTC deadline", "2026-08-19T12:01:30Z", "2026-08-19T20:01:30+08:00"},
		{"fractional deadline", "2026-08-19T12:01:30Z", "2026-08-19T12:01:30.000Z"},
		{"non UTC lease expiry", "2026-08-19T12:05:00Z", "2026-08-19T20:05:00+08:00"},
		{"fractional lease expiry", "2026-08-19T12:05:00Z", "2026-08-19T12:05:00.000Z"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Replace(valid, test.old, test.new, 1)
			if body == valid {
				t.Fatalf("mutation did not apply: %q", test.old)
			}
			if _, err := DecodeCommandAt([]byte(body), rootTerminalNow, false); err == nil {
				t.Fatal("expected terminal payload rejection")
			}
		})
	}
}

func TestDecodeCommandAtRejectsDuplicateFields(t *testing.T) {
	data, err := os.ReadFile(rootTerminalFixture)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{"envelope", `"version": 1`, `"version": 1, "version": 1`},
		{"payload", `"idleTimeoutSeconds": 600`, `"idleTimeoutSeconds": 600, "idleTimeoutSeconds": 600`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Replace(valid, test.old, test.new, 1)
			if _, err := DecodeCommandAt([]byte(body), rootTerminalNow, false); err == nil {
				t.Fatal("expected duplicate field rejection")
			}
		})
	}
}

func TestDecodeCommandAtAllowsOnlyExplicitInsecureLoopback(t *testing.T) {
	data, err := os.ReadFile(rootTerminalFixture)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	loopback := strings.Replace(valid,
		"wss://management.example/", "ws://127.0.0.1:19147/", 1)
	if _, err := DecodeCommandAt([]byte(loopback), rootTerminalNow, true); err != nil {
		t.Fatalf("explicit local relay rejected: %v", err)
	}
	if _, err := DecodeCommandAt([]byte(loopback), rootTerminalNow, false); err == nil {
		t.Fatal("production accepted insecure local relay")
	}
	for _, host := range []string{"management.example", "127.0.0.2", "0.0.0.0"} {
		body := strings.Replace(loopback, "127.0.0.1", host, 1)
		if _, err := DecodeCommandAt([]byte(body), rootTerminalNow, true); err == nil {
			t.Fatalf("insecure host %q accepted", host)
		}
	}
}

func TestDecodeDirectImageDeployment(t *testing.T) {
	body := `{"commandId":"11111111-2222-4333-8444-555555555555","type":"DEPLOY_IMAGE","version":1,"leaseToken":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","leaseExpiresAt":"2026-08-19T12:05:00Z","payload":{"agentId":"11111111-2222-4333-8444-555555555555","deploymentId":"22222222-3333-4444-8555-666666666666","imageName":"xkp/anno:v1","overwrite":true,"idempotencyKey":"request-1"}}`
	command, err := DecodeCommandAt([]byte(body), time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), false)
	if err != nil {
		t.Fatal(err)
	}
	if command.ImageDeployment.ImageName != "xkp/anno:v1" || !command.ImageDeployment.Overwrite {
		t.Fatalf("payload=%+v", command.ImageDeployment)
	}
}
