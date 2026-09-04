package modeldeploy

import (
	"bytes"
	"context"
	"fmt"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/stdcopy"
)

type dockerAPI interface {
	ContainerExecCreate(context.Context, string, types.ExecConfig) (types.IDResponse, error)
	ContainerExecAttach(context.Context, string, types.ExecStartCheck) (types.HijackedResponse, error)
	ContainerExecInspect(context.Context, string) (types.ContainerExecInspect, error)
}

type DockerRunner struct{ api dockerAPI }

func NewDockerRunner(api dockerAPI) *DockerRunner { return &DockerRunner{api: api} }

func (r *DockerRunner) Run(ctx context.Context, container string, command []string) (string, error) {
	created, err := r.api.ContainerExecCreate(ctx, container, types.ExecConfig{
		Cmd: command, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return "", err
	}
	attached, err := r.api.ContainerExecAttach(ctx, created.ID, types.ExecStartCheck{})
	if err != nil {
		return "", err
	}
	defer attached.Close()
	var stdout, stderr bytes.Buffer
	if _, err = stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil {
		return "", err
	}
	result, err := r.api.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("container command failed: %s", stderr.String())
	}
	return stdout.String(), nil
}
