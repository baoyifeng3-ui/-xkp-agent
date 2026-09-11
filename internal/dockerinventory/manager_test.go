package dockerinventory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"xkp-agent/internal/protocol"
)

type fakeDocker struct {
	Docker
	containers                     []types.Container
	labels                         map[string]string
	removedImage, removedContainer bool
}

func (f *fakeDocker) ImageInspectWithRaw(context.Context, string) (types.ImageInspect, []byte, error) {
	return types.ImageInspect{ID: "sha256:immutable", RepoTags: []string{"repo:latest"}, Size: 12, Created: "2026-09-09T00:00:00.123456789Z"}, nil, nil
}
func (f *fakeDocker) ContainerList(_ context.Context, o types.ContainerListOptions) ([]types.Container, error) {
	if !o.All {
		panic("must include stopped containers")
	}
	return f.containers, nil
}
func (f *fakeDocker) ContainerInspect(context.Context, string) (types.ContainerJSON, error) {
	return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{ID: "container-id"}, Config: &container.Config{Labels: f.labels}}, nil
}
func (f *fakeDocker) ImageRemove(_ context.Context, id string, o types.ImageRemoveOptions) ([]types.ImageDeleteResponseItem, error) {
	if o.Force || id != "sha256:immutable" {
		panic("unsafe image removal")
	}
	f.removedImage = true
	return nil, nil
}
func (f *fakeDocker) ContainerRemove(_ context.Context, id string, o types.ContainerRemoveOptions) error {
	if o.RemoveVolumes || id != "container-id" {
		panic("unsafe container removal")
	}
	f.removedContainer = true
	return nil
}

func TestInventorySafetyAndDetails(t *testing.T) {
	f := &fakeDocker{containers: []types.Container{{ID: "stopped", ImageID: "sha256:immutable", State: "exited"}}}
	m := New(f)
	p := protocol.DockerInventoryPayload{Action: "DELETE_IMAGE", Target: "repo:latest"}
	if _, err := m.Execute(context.Background(), p); err == nil || !strings.Contains(err.Error(), "请先删除容器") || f.removedImage {
		t.Fatalf("referenced image deletion: %v", err)
	}
	f.containers = nil
	if _, err := m.Execute(context.Background(), p); err != nil || !f.removedImage {
		t.Fatalf("delete image: %v", err)
	}
	p.Action = "INSPECT_IMAGE"
	details, err := m.Execute(context.Background(), p)
	if err != nil || details["id"] != "sha256:immutable" || details["repository"] != "repo" || details["tag"] != "latest" || details["sizeBytes"] != int64(12) {
		t.Fatalf("inspect: %v %v", details, err)
	}
	if details["created"] != time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("created must be epoch seconds: %v", details["created"])
	}
	if tags, ok := details["repoTags"].([]string); !ok || len(tags) != 1 || tags[0] != "repo:latest" {
		t.Fatalf("missing image aliases: %v", details)
	}
	p.Target = "sha256:" + strings.Repeat("a", 64)
	details, err = m.Execute(context.Background(), p)
	if err != nil || details["repository"] != "" || details["tag"] != "" {
		t.Fatalf("image ID unexpectedly resolved to tag: %v %v", details, err)
	}
	p.Target = "docker.io/library/repo:latest"
	details, err = m.Execute(context.Background(), p)
	if err != nil || details["repository"] != "docker.io/library/repo" {
		t.Fatalf("full repository changed: %v %v", details, err)
	}
	p.Action, p.Target = "DELETE_CONTAINER", "container-id"
	f.labels = map[string]string{"com.xkp.environment.id": "environment"}
	if _, err := m.Execute(context.Background(), p); err == nil || f.removedContainer {
		t.Fatal("deleted platform container")
	}
	f.labels = nil
	if _, err := m.Execute(context.Background(), p); err != nil || !f.removedContainer {
		t.Fatalf("delete unmanaged: %v", err)
	}
	for _, target := range []string{"--force", "repo;rm", "repo\nlatest", "sha256:bad"} {
		if err := (protocol.DockerInventoryPayload{Action: "DELETE_IMAGE", Target: target}).Validate(); err == nil {
			t.Fatalf("accepted %q", target)
		}
	}
}
