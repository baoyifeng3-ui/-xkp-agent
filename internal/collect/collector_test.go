package collect

import (
	"context"
	"errors"
	"testing"
)

type fakeCollector struct {
	name     string
	snapshot Snapshot
	err      error
}

func (f fakeCollector) Name() string                              { return f.name }
func (f fakeCollector) Collect(context.Context) (Snapshot, error) { return f.snapshot, f.err }

func TestGatherPreservesSuccessfulCollectorsAndStableErrors(t *testing.T) {
	cpu := 25.0
	got := Gather(context.Background(),
		fakeCollector{name: "system", snapshot: Snapshot{CPUPercent: &cpu}},
		fakeCollector{name: "gpu", err: NewCollectorError("GPU_UNAVAILABLE", errors.New("driver missing"))},
		fakeCollector{name: "docker", err: NewCollectorError("DOCKER_UNAVAILABLE", errors.New("socket missing"))})
	if got.CPUPercent == nil || *got.CPUPercent != 25 {
		t.Fatal("system metric was suppressed")
	}
	if got.CollectorErrors["gpu"] != "GPU_UNAVAILABLE" || got.CollectorErrors["docker"] != "DOCKER_UNAVAILABLE" {
		t.Fatalf("errors = %#v", got.CollectorErrors)
	}
}
