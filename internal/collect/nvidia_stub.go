//go:build !linux

package collect

import (
	"context"
	"errors"
)

type NvidiaCollector struct{}

func NewNvidiaCollector() *NvidiaCollector { return &NvidiaCollector{} }
func (c *NvidiaCollector) Name() string    { return "gpu" }
func (c *NvidiaCollector) Collect(context.Context) (Snapshot, error) {
	return Snapshot{}, NewCollectorError("GPU_UNAVAILABLE",
		errors.New("NVML collection is supported on Linux only"))
}
