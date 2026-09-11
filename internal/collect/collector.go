package collect

import (
	"context"
	"errors"
	"strings"
	"sync"
)

type Snapshot struct {
	CPUPercent                   *float64          `json:"cpuPercent"`
	RAMTotalBytes                *uint64           `json:"ramTotalBytes,omitempty"`
	RAMUsedBytes                 *uint64           `json:"ramUsedBytes,omitempty"`
	RAMPercent                   *float64          `json:"ramPercent"`
	GPUModel                     string            `json:"gpuModel,omitempty"`
	GPUPercent                   *float64          `json:"gpuPercent"`
	GPUTemperatureCelsius        *uint32           `json:"gpuTemperatureCelsius,omitempty"`
	GPUMemoryTotalBytes          *uint64           `json:"gpuMemoryTotalBytes,omitempty"`
	GPUMemoryUsedBytes           *uint64           `json:"gpuMemoryUsedBytes,omitempty"`
	GPUMemoryPercent             *float64          `json:"gpuMemoryPercent"`
	SystemDiskTotalBytes         *uint64           `json:"systemDiskTotalBytes,omitempty"`
	SystemDiskUsedBytes          *uint64           `json:"systemDiskUsedBytes,omitempty"`
	SystemDiskPercent            *float64          `json:"systemDiskPercent"`
	WorkspaceDiskTotalBytes      *uint64           `json:"workspaceDiskTotalBytes,omitempty"`
	WorkspaceDiskUsedBytes       *uint64           `json:"workspaceDiskUsedBytes,omitempty"`
	WorkspaceDiskPercent         *float64          `json:"workspaceDiskPercent"`
	DockerAvailable              *bool             `json:"dockerAvailable"`
	DockerVersion                string            `json:"dockerVersion,omitempty"`
	RunningEnvironmentCount      *int              `json:"runningEnvironmentCount"`
	RunningContainerCount        *int              `json:"runningContainerCount"`
	NetworkReceiveBytesPerSecond *int64            `json:"networkReceiveBytesPerSecond"`
	NetworkSendBytesPerSecond    *int64            `json:"networkSendBytesPerSecond"`
	CollectorErrors              map[string]string `json:"collectorErrors"`
	Images                       []DockerImage     `json:"images,omitempty"`
	Containers                   []DockerContainer `json:"containers,omitempty"`
}

type DockerImage struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	ID         string `json:"id"`
	Digest     string `json:"digest,omitempty"`
	SizeBytes  int64  `json:"sizeBytes"`
	Created    int64  `json:"created"`
}
type DockerContainer struct {
	Name    string `json:"name"`
	ID      string `json:"id"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Created int64  `json:"created"`
}

type Collector interface {
	Name() string
	Collect(context.Context) (Snapshot, error)
}

type CollectorError struct {
	Code string
	Err  error
}

func (e *CollectorError) Error() string              { return e.Code }
func (e *CollectorError) Unwrap() error              { return e.Err }
func NewCollectorError(code string, err error) error { return &CollectorError{Code: code, Err: err} }

func Gather(ctx context.Context, collectors ...Collector) Snapshot {
	type result struct {
		name     string
		snapshot Snapshot
		err      error
	}
	results := make(chan result, len(collectors))
	var wg sync.WaitGroup
	for _, collector := range collectors {
		wg.Add(1)
		go func(c Collector) {
			defer wg.Done()
			snapshot, err := c.Collect(ctx)
			results <- result{c.Name(), snapshot, err}
		}(collector)
	}
	wg.Wait()
	close(results)
	combined := Snapshot{CollectorErrors: map[string]string{}}
	for result := range results {
		merge(&combined, result.snapshot)
		if result.err != nil {
			var collectorError *CollectorError
			if errors.As(result.err, &collectorError) {
				combined.CollectorErrors[result.name] = collectorError.Code
			} else {
				combined.CollectorErrors[result.name] = strings.ToUpper(result.name) + "_UNAVAILABLE"
			}
		}
	}
	return combined
}

func merge(to *Snapshot, from Snapshot) {
	if from.CPUPercent != nil {
		to.CPUPercent = from.CPUPercent
	}
	if from.RAMTotalBytes != nil {
		to.RAMTotalBytes = from.RAMTotalBytes
	}
	if from.RAMUsedBytes != nil {
		to.RAMUsedBytes = from.RAMUsedBytes
	}
	if from.RAMPercent != nil {
		to.RAMPercent = from.RAMPercent
	}
	if from.GPUModel != "" {
		to.GPUModel = from.GPUModel
	}
	if from.GPUPercent != nil {
		to.GPUPercent = from.GPUPercent
	}
	if from.GPUTemperatureCelsius != nil {
		to.GPUTemperatureCelsius = from.GPUTemperatureCelsius
	}
	if from.GPUMemoryTotalBytes != nil {
		to.GPUMemoryTotalBytes = from.GPUMemoryTotalBytes
	}
	if from.GPUMemoryUsedBytes != nil {
		to.GPUMemoryUsedBytes = from.GPUMemoryUsedBytes
	}
	if from.GPUMemoryPercent != nil {
		to.GPUMemoryPercent = from.GPUMemoryPercent
	}
	if from.SystemDiskTotalBytes != nil {
		to.SystemDiskTotalBytes = from.SystemDiskTotalBytes
	}
	if from.SystemDiskUsedBytes != nil {
		to.SystemDiskUsedBytes = from.SystemDiskUsedBytes
	}
	if from.SystemDiskPercent != nil {
		to.SystemDiskPercent = from.SystemDiskPercent
	}
	if from.WorkspaceDiskTotalBytes != nil {
		to.WorkspaceDiskTotalBytes = from.WorkspaceDiskTotalBytes
	}
	if from.WorkspaceDiskUsedBytes != nil {
		to.WorkspaceDiskUsedBytes = from.WorkspaceDiskUsedBytes
	}
	if from.WorkspaceDiskPercent != nil {
		to.WorkspaceDiskPercent = from.WorkspaceDiskPercent
	}
	if from.DockerAvailable != nil {
		to.DockerAvailable = from.DockerAvailable
	}
	if from.DockerVersion != "" {
		to.DockerVersion = from.DockerVersion
	}
	if from.RunningEnvironmentCount != nil {
		to.RunningEnvironmentCount = from.RunningEnvironmentCount
	}
	if from.RunningContainerCount != nil {
		to.RunningContainerCount = from.RunningContainerCount
	}
	if from.NetworkReceiveBytesPerSecond != nil {
		to.NetworkReceiveBytesPerSecond = from.NetworkReceiveBytesPerSecond
	}
	if from.NetworkSendBytesPerSecond != nil {
		to.NetworkSendBytesPerSecond = from.NetworkSendBytesPerSecond
	}
	if from.Images != nil {
		to.Images = from.Images
	}
	if from.Containers != nil {
		to.Containers = from.Containers
	}
}
