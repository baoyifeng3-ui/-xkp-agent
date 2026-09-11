package collect

import (
	"context"
	"errors"
	"github.com/docker/docker/api/types"
	"testing"
)

func TestDockerCollectorReportsStableUnavailableCode(t *testing.T) {
	c := NewDockerCollector()
	c.collect = func(context.Context) (Snapshot, error) { return Snapshot{}, errors.New("daemon unavailable") }
	got := Gather(context.Background(), c)
	if got.CollectorErrors["docker"] != "DOCKER_UNAVAILABLE" {
		t.Fatalf("errors = %#v", got.CollectorErrors)
	}
}

func TestDockerInventoryProjection(t *testing.T) {
	images := projectImages([]types.ImageSummary{{ID: "sha256:abc", RepoTags: []string{"xkp/anno:v1"}, RepoDigests: []string{"xkp/anno@sha256:def"}, Size: 42, Created: 7}})
	containers := projectContainers([]types.Container{{ID: "1234567890abcdef", Names: []string{"/anno"}, Image: "xkp/anno:v1", State: "running", Status: "Up 1 minute", Created: 8}})
	if len(images) != 1 || images[0].Repository != "xkp/anno" || images[0].Tag != "v1" || images[0].SizeBytes != 42 {
		t.Fatalf("images=%+v", images)
	}
	if len(containers) != 1 || containers[0].Name != "anno" || containers[0].State != "running" {
		t.Fatalf("containers=%+v", containers)
	}
}
