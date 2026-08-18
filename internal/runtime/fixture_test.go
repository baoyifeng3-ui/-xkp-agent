package runtime

import (
	"encoding/json"
	"os"
	"testing"
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
