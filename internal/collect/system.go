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
	snapshot := Snapshot{}
	cpuValue, err := c.cpuPercent(ctx)
	if err != nil {
		return snapshot, NewCollectorError("SYSTEM_UNAVAILABLE", err)
	}
	snapshot.CPUPercent = &cpuValue
	total, used, ramValue, err := c.memory(ctx)
	if err != nil {
		return snapshot, NewCollectorError("SYSTEM_UNAVAILABLE", err)
	}
	snapshot.RAMTotalBytes = &total
	snapshot.RAMUsedBytes = &used
	snapshot.RAMPercent = &ramValue
	systemDisk, err := c.diskUsage("/")
	if err != nil {
		return snapshot, NewCollectorError("SYSTEM_DISK_UNAVAILABLE", err)
	}
	systemPercent := clamp(systemDisk.UsedPercent)
	snapshot.SystemDiskTotalBytes = &systemDisk.Total
	snapshot.SystemDiskUsedBytes = &systemDisk.Used
	snapshot.SystemDiskPercent = &systemPercent
	workspaceDisk, err := c.diskUsage(c.workspace)
	if err != nil {
		return snapshot, NewCollectorError("WORKSPACE_DISK_UNAVAILABLE", err)
	}
	workspacePercent := clamp(workspaceDisk.UsedPercent)
	snapshot.WorkspaceDiskTotalBytes = &workspaceDisk.Total
	snapshot.WorkspaceDiskUsedBytes = &workspaceDisk.Used
	snapshot.WorkspaceDiskPercent = &workspacePercent
	return snapshot, nil
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
