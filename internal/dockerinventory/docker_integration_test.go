package dockerinventory

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"xkp-agent/internal/protocol"
)

// Opt in against a local test daemon; creates and removes only unique fixtures.
func TestDockerMultiTagDeletion(t *testing.T) {
	if os.Getenv("XKP_DOCKER_INTEGRATION") != "1" {
		t.Skip("set XKP_DOCKER_INTEGRATION=1 with a local test Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	name := fmt.Sprintf("xkp-qa-delete-%d", time.Now().UnixNano())
	first, second := name+":first", name+":second"
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "qa-marker", Mode: 0600, Size: int64(len(name))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(name)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	response, err := d.ImageImport(ctx, types.ImageImportSource{Source: bytes.NewReader(archive.Bytes()), SourceName: "-"}, first, types.ImageImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, ref := range []string{first, second} {
			if _, err := d.ImageRemove(context.Background(), ref, types.ImageRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
				t.Errorf("fixture cleanup: %v", err)
			}
		}
	}()
	_, readErr := io.Copy(io.Discard, response)
	response.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	img, _, err := d.ImageInspectWithRaw(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ImageTag(ctx, img.ID, second); err != nil {
		t.Fatal(err)
	}
	c, err := d.ContainerCreate(ctx, &container.Config{Image: first, Cmd: []string{"/unused"}}, nil, nil, nil, name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := d.ContainerRemove(context.Background(), c.ID, types.ContainerRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			t.Errorf("container cleanup: %v", err)
		}
	}()
	m := New(d)
	p := protocol.DockerInventoryPayload{Action: "DELETE_IMAGE", Target: img.ID}
	if _, err := m.Execute(ctx, p); err == nil {
		t.Fatal("accepted image used by stopped container")
	}
	current, _, err := d.ImageInspectWithRaw(ctx, img.ID)
	if err != nil || len(current.RepoTags) != 2 {
		t.Fatalf("reference protection changed tags: %v %v", current.RepoTags, err)
	}
	if err := d.ContainerRemove(ctx, c.ID, types.ContainerRemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Execute(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{first, second, img.ID} {
		if _, _, err := d.ImageInspectWithRaw(ctx, ref); !errdefs.IsNotFound(err) {
			t.Fatalf("image or tag still present: %s: %v", ref, err)
		}
	}
}
