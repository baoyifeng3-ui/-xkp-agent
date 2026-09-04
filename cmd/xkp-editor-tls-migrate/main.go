package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

const tlsDir = "/etc/xkp-agent/code-server-tls"

func main() {
	ctx := context.Background()
	api, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		fail(err)
	}
	defer api.Close()

	rows, err := api.ContainerList(ctx, types.ContainerListOptions{All: true, Filters: filters.NewArgs(
		filters.Arg("label", "com.xkp.managed=true"),
		filters.Arg("label", "com.xkp.component.type=EDITOR"),
	)})
	if err != nil {
		fail(err)
	}

	migrated := 0
	for _, row := range rows {
		inspect, err := api.ContainerInspect(ctx, row.ID)
		if err != nil {
			fail(err)
		}
		if mounted(inspect.Mounts, "/root/.config/code-cert.pem") && mounted(inspect.Mounts, "/root/.config/code-cert-key.pem") {
			continue
		}
		name := strings.TrimPrefix(inspect.Name, "/")
		backup := name + ".tls-backup"
		wasRunning := inspect.State != nil && inspect.State.Running
		if wasRunning {
			if err := api.ContainerStop(ctx, row.ID, dockercontainer.StopOptions{}); err != nil {
				fail(fmt.Errorf("stop %s: %w", name, err))
			}
		}
		_ = api.ContainerRemove(ctx, backup, types.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
		if err := api.ContainerRename(ctx, row.ID, backup); err != nil {
			fail(fmt.Errorf("rename %s: %w", name, err))
		}
		inspect.HostConfig.Binds = append(inspect.HostConfig.Binds,
			tlsDir+"/code-cert.pem:/root/.config/code-cert.pem:ro",
			tlsDir+"/code-cert-key.pem:/root/.config/code-cert-key.pem:ro")
		created, err := api.ContainerCreate(ctx, inspect.Config, inspect.HostConfig, nil, nil, name)
		if err != nil {
			rollback(ctx, api, backup, name, wasRunning)
			fail(fmt.Errorf("create %s: %w", name, err))
		}
		if wasRunning {
			if err := api.ContainerStart(ctx, created.ID, types.ContainerStartOptions{}); err != nil {
				_ = api.ContainerRemove(ctx, created.ID, types.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
				rollback(ctx, api, backup, name, true)
				fail(fmt.Errorf("start %s: %w", name, err))
			}
		}
		if err := api.ContainerRemove(ctx, backup, types.ContainerRemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
			fail(err)
		}
		fmt.Println("migrated", name)
		migrated++
	}
	fmt.Printf("complete: %d migrated\n", migrated)
}

func mounted(mounts []types.MountPoint, destination string) bool {
	for _, mount := range mounts {
		if mount.Destination == destination {
			return true
		}
	}
	return false
}

func rollback(ctx context.Context, api *client.Client, backup, name string, start bool) {
	_ = api.ContainerRename(ctx, backup, name)
	if start {
		_ = api.ContainerStart(ctx, name, types.ContainerStartOptions{})
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
