package container

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/opencontainers/image-spec/specs-go/v1"
)

type createCall struct {
	config     *dockercontainer.Config
	hostConfig *dockercontainer.HostConfig
	name       string
}

type fakeDockerAPI struct {
	containers map[string]types.ContainerJSON
	creates    []createCall
	starts     []string
	stops      []string
	removes    []string
	failCreate string
}

func (f *fakeDockerAPI) ContainerInspect(_ context.Context, name string) (types.ContainerJSON, error) {
	if value, ok := f.containers[name]; ok {
		return value, nil
	}
	return types.ContainerJSON{}, notFoundError{name: name}
}

func (f *fakeDockerAPI) ContainerCreate(_ context.Context, config *dockercontainer.Config,
	hostConfig *dockercontainer.HostConfig, _ *network.NetworkingConfig, _ *v1.Platform,
	name string) (dockercontainer.CreateResponse, error) {
	if name == f.failCreate {
		return dockercontainer.CreateResponse{}, errors.New("create failed")
	}
	f.creates = append(f.creates, createCall{config: config, hostConfig: hostConfig, name: name})
	f.containers[name] = types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{Name: "/" + name, State: &types.ContainerState{Running: false}},
		Config:            &dockercontainer.Config{Labels: config.Labels},
	}
	return dockercontainer.CreateResponse{ID: name + "-id"}, nil
}

func (f *fakeDockerAPI) ContainerStart(_ context.Context, name string, _ types.ContainerStartOptions) error {
	f.starts = append(f.starts, name)
	return nil
}

func (f *fakeDockerAPI) ContainerStop(_ context.Context, name string, _ dockercontainer.StopOptions) error {
	f.stops = append(f.stops, name)
	return nil
}

func (f *fakeDockerAPI) ContainerRemove(_ context.Context, name string, _ types.ContainerRemoveOptions) error {
	f.removes = append(f.removes, name)
	delete(f.containers, name)
	return nil
}

func TestDockerExecutorCreatesStoppedPairWithSharedWorkspaceAndTypedLimits(t *testing.T) {
	api := &fakeDockerAPI{containers: map[string]types.ContainerJSON{}}
	executor := NewDockerExecutor(api, Validator{WorkspaceRoot: t.TempDir()})
	payload := validPayload()

	result, err := executor.CreatePair(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.creates) != 2 || len(api.starts) != 0 {
		t.Fatalf("creates = %d, starts = %d", len(api.creates), len(api.starts))
	}
	annotation, editor := api.creates[0], api.creates[1]
	if annotation.hostConfig.Runtime != "sysbox-runc" || editor.hostConfig.Runtime != "nvidia" {
		t.Fatalf("runtimes = %q, %q", annotation.hostConfig.Runtime, editor.hostConfig.Runtime)
	}
	annotationSource := strings.Split(annotation.hostConfig.Binds[0], ":")[0]
	editorSource := strings.Split(editor.hostConfig.Binds[0], ":")[0]
	if annotationSource != editorSource {
		t.Fatalf("workspace sources differ: %q != %q", annotationSource, editorSource)
	}
	if editor.hostConfig.Resources.Memory != 6*1024*1024*1024 || len(editor.hostConfig.Resources.DeviceRequests) != 1 {
		t.Fatalf("editor resources = %#v", editor.hostConfig.Resources)
	}
	if result.Annotation.State != StateStopped || result.Editor.State != StateStopped {
		t.Fatalf("result = %#v", result)
	}
}

func TestDockerExecutorCompensatesWhenSecondCreateFails(t *testing.T) {
	payload := validPayload()
	api := &fakeDockerAPI{containers: map[string]types.ContainerJSON{}, failCreate: payload.Components[1].ContainerName}
	executor := NewDockerExecutor(api, Validator{WorkspaceRoot: t.TempDir()})

	if _, err := executor.CreatePair(context.Background(), payload); err == nil {
		t.Fatal("expected create failure")
	}
	if len(api.removes) != 1 || api.removes[0] != payload.Components[0].ContainerName {
		t.Fatalf("removes = %#v", api.removes)
	}
}

func TestDockerExecutorRefusesToControlMismatchedSameNameContainer(t *testing.T) {
	payload := validPayload()
	api := &fakeDockerAPI{containers: map[string]types.ContainerJSON{
		payload.Components[0].ContainerName: managedContainer(payload.Components[0].ContainerName, "different", false),
		payload.Components[1].ContainerName: managedContainer(payload.Components[1].ContainerName, payload.Components[1].ConfigFingerprint, false),
	}}
	executor := NewDockerExecutor(api, Validator{WorkspaceRoot: t.TempDir()})

	if _, err := executor.StartPair(context.Background(), payload); err == nil {
		t.Fatal("expected fingerprint mismatch")
	}
	if len(api.starts) != 0 {
		t.Fatalf("unexpected starts = %#v", api.starts)
	}
}

type notFoundError struct{ name string }

func (e notFoundError) Error() string { return "No such container: " + e.name }

func managedContainer(name, fingerprint string, running bool) types.ContainerJSON {
	return types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{Name: "/" + name, State: &types.ContainerState{Running: running}},
		Config: &dockercontainer.Config{Labels: map[string]string{
			labelManaged: "true", labelFingerprint: fingerprint,
		}},
	}
}
