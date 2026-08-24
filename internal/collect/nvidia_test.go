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

func TestParseNvidiaSMIOutput(t *testing.T) {
	got, err := parseNvidiaSMI("NVIDIA RTX A4000, 37, 16384, 4096, 54\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.GPUModel != "NVIDIA RTX A4000" || got.GPUPercent == nil || *got.GPUPercent != 37 {
		t.Fatalf("snapshot = %#v", got)
	}
	if got.GPUMemoryTotalBytes == nil || *got.GPUMemoryTotalBytes != 16384*1024*1024 {
		t.Fatalf("total memory = %#v", got.GPUMemoryTotalBytes)
	}
	if got.GPUMemoryPercent == nil || *got.GPUMemoryPercent != 25 {
		t.Fatalf("memory percent = %#v", got.GPUMemoryPercent)
	}
	if got.GPUTemperatureCelsius == nil || *got.GPUTemperatureCelsius != 54 {
		t.Fatalf("temperature = %#v", got.GPUTemperatureCelsius)
	}
}
