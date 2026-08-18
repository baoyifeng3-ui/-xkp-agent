//go:build linux

package collect

import (
	"context"
	"fmt"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

type NvidiaCollector struct{ collect func() (Snapshot, error) }

func NewNvidiaCollector() *NvidiaCollector {
	c := &NvidiaCollector{}
	c.collect = collectNVML
	return c
}
func (c *NvidiaCollector) Name() string { return "gpu" }
func (c *NvidiaCollector) Collect(context.Context) (Snapshot, error) {
	snapshot, err := c.collect()
	if err != nil {
		return Snapshot{}, NewCollectorError("GPU_UNAVAILABLE", err)
	}
	return snapshot, nil
}

func collectNVML() (Snapshot, error) {
	if result := nvml.Init(); result != nvml.SUCCESS {
		return Snapshot{}, fmt.Errorf("NVML init: %s", nvml.ErrorString(result))
	}
	defer nvml.Shutdown()
	device, result := nvml.DeviceGetHandleByIndex(0)
	if result != nvml.SUCCESS {
		return Snapshot{}, fmt.Errorf("GPU handle: %s", nvml.ErrorString(result))
	}
	name, result := device.GetName()
	if result != nvml.SUCCESS {
		return Snapshot{}, fmt.Errorf("GPU name: %s", nvml.ErrorString(result))
	}
	utilization, result := device.GetUtilizationRates()
	if result != nvml.SUCCESS {
		return Snapshot{}, fmt.Errorf("GPU utilization: %s", nvml.ErrorString(result))
	}
	memory, result := device.GetMemoryInfo()
	if result != nvml.SUCCESS {
		return Snapshot{}, fmt.Errorf("GPU memory: %s", nvml.ErrorString(result))
	}
	temperature, result := device.GetTemperature(nvml.TEMPERATURE_GPU)
	if result != nvml.SUCCESS {
		return Snapshot{}, fmt.Errorf("GPU temperature: %s", nvml.ErrorString(result))
	}
	gpuPercent := clamp(float64(utilization.Gpu))
	memoryPercent := 0.0
	if memory.Total > 0 {
		memoryPercent = clamp(float64(memory.Used) * 100 / float64(memory.Total))
	}
	return Snapshot{GPUModel: name, GPUPercent: &gpuPercent, GPUTemperatureCelsius: &temperature, GPUMemoryTotalBytes: &memory.Total, GPUMemoryUsedBytes: &memory.Used, GPUMemoryPercent: &memoryPercent}, nil
}
