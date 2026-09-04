package container

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MPSManager prepares one shared MPS control endpoint for the Agent host.
type MPSManager interface {
	EnsureRunning(context.Context) (string, error)
}

type HostMPSManager struct {
	directory string
}

func NewHostMPSManager(directory string) *HostMPSManager {
	return &HostMPSManager{directory: directory}
}

func (m *HostMPSManager) EnsureRunning(ctx context.Context) (string, error) {
	if m == nil || filepath.IsAbs(m.directory) == false {
		return "", fmt.Errorf("MPS directory must be absolute")
	}
	if _, err := exec.LookPath("nvidia-cuda-mps-control"); err != nil {
		return "", fmt.Errorf("MPS control service is unavailable: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(m.directory, "log"), 0750); err != nil {
		return "", fmt.Errorf("create MPS directory: %w", err)
	}
	if _, statErr := os.Stat(filepath.Join(m.directory, "control")); statErr == nil {
		probe := exec.CommandContext(ctx, "nvidia-cuda-mps-control")
		probe.Env = append(os.Environ(), "CUDA_MPS_PIPE_DIRECTORY="+m.directory)
		probe.Stdin = strings.NewReader("get_server_list\n")
		if err := probe.Run(); err == nil {
			return m.directory, nil
		}
	}
	command := exec.CommandContext(ctx, "nvidia-cuda-mps-control", "-d")
	command.Env = append(os.Environ(),
		"CUDA_MPS_PIPE_DIRECTORY="+m.directory,
		"CUDA_MPS_LOG_DIRECTORY="+filepath.Join(m.directory, "log"))
	if output, err := command.CombinedOutput(); err != nil {
		return "", fmt.Errorf("start MPS control service: %w (%s)", err, string(output))
	}
	return m.directory, nil
}
