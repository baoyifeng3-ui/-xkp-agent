//go:build linux

package collect

import (
	"context"
	"encoding/csv"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type NvidiaCollector struct{ collect func() (Snapshot, error) }

func NewNvidiaCollector() *NvidiaCollector {
	c := &NvidiaCollector{}
	c.collect = collectNvidiaSMI
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

func collectNvidiaSMI() (Snapshot, error) {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil { return Snapshot{}, fmt.Errorf("nvidia-smi unavailable: %w", err) }
	output, err := exec.Command(path, "--query-gpu=name,utilization.gpu,memory.total,memory.used,temperature.gpu", "--format=csv,noheader,nounits").Output()
	if err != nil { return Snapshot{}, fmt.Errorf("nvidia-smi query failed: %w", err) }
	return parseNvidiaSMI(string(output))
}

func parseNvidiaSMI(output string) (Snapshot, error) {
	records, err := csv.NewReader(strings.NewReader(strings.TrimSpace(output))).ReadAll()
	if err != nil || len(records) == 0 || len(records[0]) != 5 { return Snapshot{}, fmt.Errorf("invalid nvidia-smi output") }
	row := records[0]
	for i := range row { row[i] = strings.TrimSpace(row[i]) }
	gpuPercent, err := strconv.ParseFloat(row[1], 64)
	if err != nil { return Snapshot{}, fmt.Errorf("invalid GPU utilization: %w", err) }
	totalMiB, err := strconv.ParseUint(row[2], 10, 64)
	if err != nil { return Snapshot{}, fmt.Errorf("invalid total GPU memory: %w", err) }
	usedMiB, err := strconv.ParseUint(row[3], 10, 64)
	if err != nil { return Snapshot{}, fmt.Errorf("invalid used GPU memory: %w", err) }
	temperature, err := strconv.ParseUint(row[4], 10, 32)
	if err != nil { return Snapshot{}, fmt.Errorf("invalid GPU temperature: %w", err) }
	totalBytes, usedBytes := totalMiB*1024*1024, usedMiB*1024*1024
	memoryPercent := 0.0
	if totalBytes > 0 { memoryPercent = clamp(float64(usedBytes) * 100 / float64(totalBytes)) }
	temperatureValue := uint32(temperature)
	gpuPercent = clamp(gpuPercent)
	return Snapshot{GPUModel: row[0], GPUPercent: &gpuPercent, GPUTemperatureCelsius: &temperatureValue, GPUMemoryTotalBytes: &totalBytes, GPUMemoryUsedBytes: &usedBytes, GPUMemoryPercent: &memoryPercent}, nil
}
