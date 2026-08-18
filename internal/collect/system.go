package collect

import (
	"context"
	"fmt"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

type SystemCollector struct {
	workspace  string
	cpuPercent func(context.Context) (float64, error)
	memory     func(context.Context) (uint64, uint64, float64, error)
	diskUsage  func(string) (*disk.UsageStat, error)
}

func NewSystemCollector(workspace string) *SystemCollector {
	c := &SystemCollector{workspace: workspace}
	c.cpuPercent = func(ctx context.Context) (float64, error) {
		values, err := cpu.PercentWithContext(ctx, 0, false)
		if err != nil || len(values) == 0 {
			return 0, fmt.Errorf("CPU collection failed: %w", err)
		}
		return clamp(values[0]), nil
	}
	c.memory = func(ctx context.Context) (uint64, uint64, float64, error) {
		value, err := mem.VirtualMemoryWithContext(ctx)
		if err != nil {
			return 0, 0, 0, err
		}
		return value.Total, value.Used, clamp(value.UsedPercent), nil
	}
	c.diskUsage = disk.Usage
	return c
}

func (c *SystemCollector) Name() string { return "system" }
func (c *SystemCollector) Collect(ctx context.Context) (Snapshot, error) {
	cpuValue, err := c.cpuPercent(ctx)
	if err != nil {
		return Snapshot{}, NewCollectorError("SYSTEM_UNAVAILABLE", err)
	}
	total, used, ramValue, err := c.memory(ctx)
	if err != nil {
		return Snapshot{}, NewCollectorError("SYSTEM_UNAVAILABLE", err)
	}
	systemDisk, err := c.diskUsage("/")
	if err != nil {
		return Snapshot{}, NewCollectorError("SYSTEM_DISK_UNAVAILABLE", err)
	}
	workspaceDisk, err := c.diskUsage(c.workspace)
	if err != nil {
		return Snapshot{}, NewCollectorError("WORKSPACE_DISK_UNAVAILABLE", err)
	}
	systemPercent, workspacePercent := clamp(systemDisk.UsedPercent), clamp(workspaceDisk.UsedPercent)
	return Snapshot{CPUPercent: &cpuValue, RAMTotalBytes: &total, RAMUsedBytes: &used, RAMPercent: &ramValue,
		SystemDiskTotalBytes: &systemDisk.Total, SystemDiskUsedBytes: &systemDisk.Used, SystemDiskPercent: &systemPercent,
		WorkspaceDiskTotalBytes: &workspaceDisk.Total, WorkspaceDiskUsedBytes: &workspaceDisk.Used, WorkspaceDiskPercent: &workspacePercent}, nil
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
