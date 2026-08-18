package collect

import (
	"context"
	"testing"

	"github.com/shirou/gopsutil/v4/disk"
)

func TestSystemCollectorKeepsSystemAndWorkspaceDisksDistinct(t *testing.T) {
	paths := []string{}
	c := NewSystemCollector("/workspace")
	c.diskUsage = func(path string) (*disk.UsageStat, error) {
		paths = append(paths, path)
		if path == "/" {
			return &disk.UsageStat{Total: 1000, Used: 200, UsedPercent: 20}, nil
		}
		return &disk.UsageStat{Total: 2000, Used: 1000, UsedPercent: 50}, nil
	}
	c.cpuPercent = func(context.Context) (float64, error) { return 10, nil }
	c.memory = func(context.Context) (uint64, uint64, float64, error) { return 100, 50, 50, nil }

	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] == paths[1] {
		t.Fatalf("disk paths = %#v", paths)
	}
	if *got.SystemDiskPercent != 20 || *got.WorkspaceDiskPercent != 50 {
		t.Fatalf("disk metrics = %#v", got)
	}
}
