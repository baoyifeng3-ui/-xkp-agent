package runtime

import (
	"encoding/json"
	"os"
	"testing"

	"xkp-agent/internal/protocol"
)

func TestHeartbeatV1FixtureMatchesRuntimeContract(t *testing.T) {
	data, err := os.ReadFile("../../testdata/heartbeat-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var request HeartbeatRequest
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	if request.Sequence != 17 || request.Metrics.GPUModel != "NVIDIA GeForce RTX 2080" {
		t.Fatalf("fixture = %#v", request)
	}
}

func TestCompetitionEnvironmentFixturesUseVersionOneCommandContract(t *testing.T) {
	tests := []struct {
		name string
		want protocol.CommandType
	}{
		{"create", protocol.CreateCompetitionEnvironment},
		{"start", protocol.StartCompetitionEnvironment},
		{"stop", protocol.StopCompetitionEnvironment},
		{"restore", protocol.RestoreCompetitionEnvironment},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := os.ReadFile("../../testdata/command-" + test.name + "-competition-environment-v1.json")
			if err != nil {
				t.Fatal(err)
			}
			command, err := protocol.DecodeCommand(data)
			if err != nil {
				t.Fatal(err)
			}
			if command.Type != test.want || command.Version != 1 || command.Environment == nil {
				t.Fatalf("command = %#v", command)
			}
		})
	}
}
