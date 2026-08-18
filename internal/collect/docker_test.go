package collect

import (
	"context"
	"errors"
	"testing"
)

func TestDockerCollectorReportsStableUnavailableCode(t *testing.T) {
	c := NewDockerCollector()
	c.collect = func(context.Context) (Snapshot, error) { return Snapshot{}, errors.New("daemon unavailable") }
	got := Gather(context.Background(), c)
	if got.CollectorErrors["docker"] != "DOCKER_UNAVAILABLE" {
		t.Fatalf("errors = %#v", got.CollectorErrors)
	}
}
