package collect

import (
	"context"
	"strings"

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
	images, err := cli.ImageList(ctx, types.ImageListOptions{})
	if err != nil {
		return Snapshot{}, err
	}
	running := 0
	environments := map[string]struct{}{}
	// Label namespace must match internal/container/docker.go's labelEnvironment
	// ("com.xkp.environment.id"); reading the unprefixed key never matches and
	// would leave RunningEnvironmentCount at zero.
	const labelEnvironment = "com.xkp.environment.id"
	for _, container := range containers {
		if container.State == "running" {
			running++
			if id := container.Labels[labelEnvironment]; id != "" {
				environments[id] = struct{}{}
			}
		}
	}
	available := true
	environmentCount := len(environments)
	return Snapshot{DockerAvailable: &available, DockerVersion: version.Version, RunningContainerCount: &running, RunningEnvironmentCount: &environmentCount, Images: projectImages(images), Containers: projectContainers(containers)}, nil
}

func projectImages(source []types.ImageSummary) []DockerImage {
	result := make([]DockerImage, 0, len(source))
	for _, image := range source {
		tags := image.RepoTags
		if len(tags) == 0 {
			tags = []string{""}
		}
		for _, reference := range tags {
			repository, tag := reference, ""
			if i := strings.LastIndex(reference, ":"); i > strings.LastIndex(reference, "/") {
				repository, tag = reference[:i], reference[i+1:]
			}
			digest := ""
			if len(image.RepoDigests) > 0 {
				digest = image.RepoDigests[0]
			}
			result = append(result, DockerImage{Repository: repository, Tag: tag, ID: image.ID, Digest: digest, SizeBytes: image.Size, Created: image.Created})
		}
	}
	return result
}

func projectContainers(source []types.Container) []DockerContainer {
	result := make([]DockerContainer, 0, len(source))
	for _, container := range source {
		name := ""
		if len(container.Names) > 0 {
			name = strings.TrimPrefix(container.Names[0], "/")
		}
		id := container.ID
		if len(id) > 12 {
			id = id[:12]
		}
		result = append(result, DockerContainer{Name: name, ID: id, Image: container.Image, State: container.State, Status: container.Status, Created: container.Created})
	}
	return result
}
