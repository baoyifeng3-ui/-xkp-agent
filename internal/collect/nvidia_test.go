package collect

import (
	"context"
	"errors"
	"testing"
)

func TestNvidiaCollectorReportsStableUnavailableCode(t *testing.T) {
	c := NewNvidiaCollector()
	c.collect = func() (Snapshot, error) { return Snapshot{}, errors.New("NVML unavailable") }
	got := Gather(context.Background(), c)
	if got.CollectorErrors["gpu"] != "GPU_UNAVAILABLE" {
		t.Fatalf("errors = %#v", got.CollectorErrors)
	}
}
