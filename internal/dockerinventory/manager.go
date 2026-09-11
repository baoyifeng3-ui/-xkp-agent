package dockerinventory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types"
	"xkp-agent/internal/protocol"
)

type Docker interface {
	ImageInspectWithRaw(context.Context, string) (types.ImageInspect, []byte, error)
	ContainerList(context.Context, types.ContainerListOptions) ([]types.Container, error)
	ContainerInspect(context.Context, string) (types.ContainerJSON, error)
	ImageRemove(context.Context, string, types.ImageRemoveOptions) ([]types.ImageDeleteResponseItem, error)
	ContainerRemove(context.Context, string, types.ContainerRemoveOptions) error
}

type Manager struct{ docker Docker }

func New(docker Docker) *Manager { return &Manager{docker: docker} }

func (m *Manager) Execute(ctx context.Context, p protocol.DockerInventoryPayload) (map[string]interface{}, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if p.Action == "DELETE_CONTAINER" {
		c, err := m.docker.ContainerInspect(ctx, p.Target)
		if err != nil {
			return nil, err
		}
		if c.Config == nil || c.ContainerJSONBase == nil || c.ID == "" {
			return nil, fmt.Errorf("容器检查结果无效")
		}
		if c.Config.Labels["com.xkp.managed"] == "true" || c.Config.Labels["com.xkp.environment.id"] != "" {
			return nil, fmt.Errorf("平台环境容器请通过环境删除操作移除")
		}
		if err := m.docker.ContainerRemove(ctx, c.ID, types.ContainerRemoveOptions{Force: true}); err != nil {
			return nil, err
		}
		return map[string]interface{}{"id": c.ID}, nil
	}
	i, _, err := m.docker.ImageInspectWithRaw(ctx, p.Target)
	if err != nil {
		return nil, err
	}
	if i.ID == "" {
		return nil, fmt.Errorf("镜像检查结果无效")
	}
	if p.Action == "DELETE_IMAGE" {
		containers, err := m.docker.ContainerList(ctx, types.ContainerListOptions{All: true})
		if err != nil {
			return nil, err
		}
		for _, c := range containers {
			if c.ImageID == i.ID {
				return nil, fmt.Errorf("镜像仍被容器 %s 使用，请先删除容器（包括已停止的容器）", c.ID)
			}
		}
		// Remove the inspected immutable ID so a concurrent retag cannot delete another image.
		if _, err := m.docker.ImageRemove(ctx, i.ID, types.ImageRemoveOptions{}); err != nil {
			return nil, err
		}
		return map[string]interface{}{"id": i.ID}, nil
	}
	repository, tag, digest := "", "", ""
	ref := p.Target
	if strings.HasPrefix(ref, "sha256:") {
		ref = ""
	}
	if parsed, err := reference.Parse(ref); err == nil {
		if named, ok := parsed.(reference.Named); ok {
			repository = named.Name()
		}
		if tagged, ok := parsed.(reference.Tagged); ok {
			tag = tagged.Tag()
		} else if !strings.Contains(ref, "@") {
			tag = "latest"
		}
	}
	if len(i.RepoDigests) > 0 {
		digest = i.RepoDigests[0]
	}
	created, err := time.Parse(time.RFC3339Nano, i.Created)
	if err != nil {
		return nil, fmt.Errorf("镜像创建时间无效: %w", err)
	}
	return map[string]interface{}{"id": i.ID, "repository": repository, "tag": tag, "digest": digest, "sizeBytes": i.Size, "created": created.Unix(), "repoTags": i.RepoTags}, nil
}
