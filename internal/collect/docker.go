package collect

import (
	"context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

type DockerCollector struct {
	collect func(context.Context) (Snapshot, error)
}

func NewDockerCollector() *DockerCollector {
	c := &DockerCollector{}
	c.collect = collectDocker
	return c
}
func (c *DockerCollector) Name() string { return "docker" }
func (c *DockerCollector) Collect(ctx context.Context) (Snapshot, error) {
	snapshot, err := c.collect(ctx)
	if err != nil {
		available := false
		snapshot.DockerAvailable = &available
		return snapshot, NewCollectorError("DOCKER_UNAVAILABLE", err)
	}
	return snapshot, nil
}

func collectDocker(ctx context.Context) (Snapshot, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return Snapshot{}, err
	}
	defer cli.Close()
	if _, err := cli.Ping(ctx); err != nil {
		return Snapshot{}, err
	}
	version, err := cli.ServerVersion(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{All: true})
	if err != nil {
		return Snapshot{}, err
	}
	running := 0
	environments := map[string]struct{}{}
	for _, container := range containers {
		if container.State == "running" {
			running++
			if id := container.Labels["xkp.environment.id"]; id != "" {
				environments[id] = struct{}{}
			}
		}
	}
	available := true
	environmentCount := len(environments)
	return Snapshot{DockerAvailable: &available, DockerVersion: version.Version, RunningContainerCount: &running, RunningEnvironmentCount: &environmentCount}, nil
}
