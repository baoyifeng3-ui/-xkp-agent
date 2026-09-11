package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	"github.com/opencontainers/image-spec/specs-go/v1"
	"xkp-agent/internal/protocol"
)

type createCall struct {
	config     *dockercontainer.Config
	hostConfig *dockercontainer.HostConfig
	name       string
}

func TestStopMissingEnvironmentIsIdempotent(t *testing.T) {
	payload := validPayload()
	executor := NewDockerExecutor(&fakeDockerAPI{containers: map[string]types.ContainerJSON{}}, Validator{WorkspaceRoot: t.TempDir()})
	result, err := executor.StopPair(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if result.Annotation.State != StateMissing || result.Editor.State != StateMissing {
		t.Fatalf("result=%+v", result)
	}
}

func testTLSDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"code-cert.pem", "code-cert-key.pem"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDockerExecutorSupportsSingleComponentLifecycle(t *testing.T) {
	for _, component := range validPayload().Components {
		t.Run(component.ComponentType, func(t *testing.T) {
			payload := validPayload()
			payload.Components = []protocol.EnvironmentComponent{component}
			api := &fakeDockerAPI{containers: map[string]types.ContainerJSON{}}
			executor := NewDockerExecutor(api, Validator{WorkspaceRoot: t.TempDir(), CodeServerTLSDir: testTLSDir(t)})
			if _, err := executor.CreatePair(context.Background(), payload); err != nil {
				t.Fatal(err)
			}
			if len(api.creates) != 1 {
				t.Fatalf("creates = %d", len(api.creates))
			}
			if _, err := executor.StartPair(context.Background(), payload); err != nil {
				t.Fatal(err)
			}
			if _, err := executor.StopPair(context.Background(), payload); err != nil {
				t.Fatal(err)
			}
			if _, err := executor.RestorePair(context.Background(), payload); err != nil {
				t.Fatal(err)
			}
			if _, err := executor.DeletePair(context.Background(), payload); err != nil {
				t.Fatal(err)
			}
			if len(api.containers) != 0 {
				t.Fatal("container not deleted")
			}
		})
	}
}

type fakeMPSManager struct{ directory string }

func (f fakeMPSManager) EnsureRunning(context.Context) (string, error) { return f.directory, nil }

type fakeDockerAPI struct {
	containers     map[string]types.ContainerJSON
	creates        []createCall
	starts         []string
	stops          []string
	removes        []string
	failCreate     string
	execContainers []string
	execConfigs    []types.ExecConfig
}

func (f *fakeDockerAPI) ContainerExecCreate(_ context.Context, container string, config types.ExecConfig) (types.IDResponse, error) {
	f.execContainers = append(f.execContainers, container)
	f.execConfigs = append(f.execConfigs, config)
	return types.IDResponse{ID: "exec-1"}, nil
}

func TestDockerExecutorInstallsCondaCompatibilityForMpsEditor(t *testing.T) {
	payload := validPayload()
	payload.Components[1].MPSEnabled = true
	payload.Components[1].GPUComputePercent = 20
	api := &fakeDockerAPI{containers: map[string]types.ContainerJSON{
		payload.Components[0].ContainerName: managedContainer(payload.Components[0].ContainerName, payload.Components[0].ConfigFingerprint, true),
		payload.Components[1].ContainerName: managedContainer(payload.Components[1].ContainerName, payload.Components[1].ConfigFingerprint, true),
	}}
	for name, value := range api.containers {
		value.Config.Labels[labelEnvironment] = payload.EnvironmentID
		value.Config.Labels[labelComponent] = map[bool]string{true: "ANNOTATION", false: "EDITOR"}[name == payload.Components[0].ContainerName]
		value.NetworkSettings = &types.NetworkSettings{NetworkSettingsBase: types.NetworkSettingsBase{Ports: nat.PortMap{}}}
		api.containers[name] = value
	}

	if _, err := NewDockerExecutor(api, Validator{WorkspaceRoot: t.TempDir()}, fakeMPSManager{directory: "/tmp/xkp-mps"}).StartPair(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	found := false
	for index, name := range api.execContainers {
		if name != payload.Components[1].ContainerName {
			continue
		}
		command := strings.Join(api.execConfigs[index].Cmd, " ")
		found = strings.Contains(command, "CONDA_EXE") &&
			strings.Contains(command, "CUDA_MPS_ACTIVE_THREAD_PERCENTAGE") &&
			strings.Contains(command, "declare -f conda") &&
			strings.Contains(command, "xkp_conda_raw") &&
			strings.Contains(command, "alias conda=xkp_conda")
	}
	if !found {
		t.Fatalf("editor conda compatibility exec missing: containers=%v configs=%#v", api.execContainers, api.execConfigs)
	}
}
func (f *fakeDockerAPI) ContainerExecStart(context.Context, string, types.ExecStartCheck) error {
	return nil
}
func (f *fakeDockerAPI) ContainerExecInspect(context.Context, string) (types.ContainerExecInspect, error) {
	return types.ContainerExecInspect{Running: false, ExitCode: 0}, nil
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
	executor := NewDockerExecutor(api, Validator{WorkspaceRoot: t.TempDir(), CodeServerTLSDir: testTLSDir(t)})
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

func TestDockerExecutorMountsOnlyEditorLeafTlsFilesReadOnly(t *testing.T) {
	payload := validPayload()
	tlsDir := t.TempDir()
	for _, name := range []string{"code-cert.pem", "code-cert-key.pem"} {
		if err := os.WriteFile(filepath.Join(tlsDir, name), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, host, err := dockerConfigs(payload.EnvironmentID, t.TempDir(), payload.Components[1], "", tlsDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		tlsDir + "/code-cert.pem:/root/.config/code-cert.pem:ro",
		tlsDir + "/code-cert-key.pem:/root/.config/code-cert-key.pem:ro",
	}
	for _, bind := range want {
		if !contains(host.Binds, bind) {
			t.Fatalf("missing TLS bind %q in %#v", bind, host.Binds)
		}
	}
	for _, bind := range host.Binds {
		if strings.Contains(bind, "ca.key") || strings.Contains(bind, "id_ed25519") || strings.Contains(bind, "id_rsa") {
			t.Fatalf("private platform key mount: %q", bind)
		}
	}
}

func TestPreflightRejectsTLSDirectory(t *testing.T) {
	tlsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tlsDir, "code-cert.pem"), []byte("certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tlsDir, "code-cert-key.pem"), 0700); err != nil {
		t.Fatal(err)
	}
	payload := validPayload()
	_, host, err := dockerConfigs(payload.EnvironmentID, t.TempDir(), payload.Components[1], "", tlsDir)
	if err == nil || !strings.Contains(err.Error(), "code-cert-key.pem must be a regular file") {
		t.Fatalf("error = %v", err)
	}
	if host != nil {
		t.Fatalf("host config created after failed TLS preflight: %#v", host)
	}
}

func TestDockerExecutorMakesSharedWorkspaceReadableByAnnotationService(t *testing.T) {
	root := t.TempDir()
	payload := validPayload()
	workspace := filepath.Join(root, filepath.FromSlash(payload.WorkspaceRelativePath))
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}

	api := &fakeDockerAPI{containers: map[string]types.ContainerJSON{}}
	if _, err := NewDockerExecutor(api, Validator{WorkspaceRoot: root, CodeServerTLSDir: testTLSDir(t)}).CreatePair(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0755 {
		t.Fatalf("workspace permissions = %04o, want 0755", got)
	}
}

func TestDockerExecutorEnablesMpsOnlyWhenComponentRequestsIt(t *testing.T) {
	tlsDir := testTLSDir(t)
	payload := validPayload()
	payload.Components[1].MPSEnabled = true
	payload.Components[1].GPUComputePercent = 35
	payload.Components[1].GPUMemoryLimitBytes = 2 * 1024 * 1024 * 1024
	api := &fakeDockerAPI{containers: map[string]types.ContainerJSON{}}
	if _, err := NewDockerExecutor(api, Validator{WorkspaceRoot: t.TempDir(), CodeServerTLSDir: tlsDir}, fakeMPSManager{directory: "/tmp/xkp-mps"}).CreatePair(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if got := api.creates[1].config.Env; len(got) != 6 || got[0] != "TF_FORCE_GPU_ALLOW_GROWTH=true" ||
		got[1] != "XKP_GPU_COMPUTE_PERCENT=35" || got[2] != "CUDA_MPS_ACTIVE_THREAD_PERCENTAGE=35" ||
		got[3] != "XKP_GPU_MEMORY_LIMIT_BYTES=2147483648" || got[4] != "CUDA_MPS_PINNED_DEVICE_MEM_LIMIT=0=2048M" ||
		got[5] != "CUDA_MPS_PIPE_DIRECTORY=/tmp/xkp-mps" {
		t.Fatalf("MPS environment = %#v", got)
	}
	if got := api.creates[1].hostConfig.Binds; len(got) != 4 || got[1] != "/tmp/xkp-mps:/tmp/xkp-mps:rw" ||
		got[2] != tlsDir+"/code-cert.pem:/root/.config/code-cert.pem:ro" ||
		got[3] != tlsDir+"/code-cert-key.pem:/root/.config/code-cert-key.pem:ro" {
		t.Fatalf("MPS bind = %#v", got)
	}
}

func TestDockerConfigsPureGpuUsesRuntimeOnly(t *testing.T) {
	payload := validPayload()
	payload.Components[1].GPUComputePercent = 0
	payload.Components[1].GPUMemoryLimitBytes = 0
	payload.Components[1].MPSEnabled = false
	tlsDir := t.TempDir()
	for _, name := range []string{"code-cert.pem", "code-cert-key.pem"} {
		if err := os.WriteFile(filepath.Join(tlsDir, name), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config, host, err := dockerConfigs(payload.EnvironmentID, t.TempDir(), payload.Components[1], "", tlsDir)
	if err != nil {
		t.Fatal(err)
	}
	if host.Runtime != "nvidia" || len(host.Resources.DeviceRequests) != 0 || len(config.Env) != 0 {
		t.Fatalf("pure GPU config = runtime %q, requests %#v, env %#v", host.Runtime, host.Resources.DeviceRequests, config.Env)
	}
}

func TestDockerExecutorCompensatesWhenSecondCreateFails(t *testing.T) {
	payload := validPayload()
	api := &fakeDockerAPI{containers: map[string]types.ContainerJSON{}, failCreate: payload.Components[1].ContainerName}
	executor := NewDockerExecutor(api, Validator{WorkspaceRoot: t.TempDir(), CodeServerTLSDir: testTLSDir(t)})

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

func TestDockerExecutorRestoreReplacesOwnedContainerWithOldFingerprint(t *testing.T) {
	payload := validPayload()
	old := managedContainer(payload.Components[1].ContainerName, "old-fingerprint", false)
	old.Config.Labels[labelManaged] = "true"
	old.Config.Labels[labelEnvironment] = payload.EnvironmentID
	old.Config.Labels[labelComponent] = payload.Components[1].ComponentType
	api := &fakeDockerAPI{containers: map[string]types.ContainerJSON{
		payload.Components[1].ContainerName: old,
	}}

	if _, err := NewDockerExecutor(api, Validator{WorkspaceRoot: t.TempDir(), CodeServerTLSDir: testTLSDir(t)}).RestorePair(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if len(api.removes) != 1 || api.removes[0] != payload.Components[1].ContainerName {
		t.Fatalf("removes = %#v", api.removes)
	}
}

func TestDockerExecutorDeletesManagedPairAndTreatsMissingAsSuccess(t *testing.T) {
	payload := validPayload()
	annotation := managedContainer(payload.Components[0].ContainerName,
		payload.Components[0].ConfigFingerprint, false)
	annotation.Config.Labels[labelEnvironment] = payload.EnvironmentID
	annotation.Config.Labels[labelComponent] = payload.Components[0].ComponentType
	annotation.Config.Labels[labelManaged] = "true"
	editor := managedContainer(payload.Components[1].ContainerName,
		payload.Components[1].ConfigFingerprint, false)
	editor.Config.Labels[labelEnvironment] = payload.EnvironmentID
	editor.Config.Labels[labelComponent] = payload.Components[1].ComponentType
	editor.Config.Labels[labelManaged] = "true"
	api := &fakeDockerAPI{containers: map[string]types.ContainerJSON{
		payload.Components[0].ContainerName: annotation,
		payload.Components[1].ContainerName: editor,
	}}
	executor := NewDockerExecutor(api, Validator{WorkspaceRoot: t.TempDir()})

	result, err := executor.DeletePair(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.removes) != 2 || result.Annotation.State != StateMissing || result.Editor.State != StateMissing {
		t.Fatalf("removes=%v result=%#v", api.removes, result)
	}
	if _, err := executor.DeletePair(context.Background(), payload); err != nil {
		t.Fatalf("repeated delete failed: %v", err)
	}
}

func TestDockerExecutorInitializesAnnotationAuthenticationAfterStart(t *testing.T) {
	payload := validPayload()
	api := &fakeDockerAPI{containers: map[string]types.ContainerJSON{
		payload.Components[0].ContainerName: managedContainer(payload.Components[0].ContainerName, payload.Components[0].ConfigFingerprint, true),
		payload.Components[1].ContainerName: managedContainer(payload.Components[1].ContainerName, payload.Components[1].ConfigFingerprint, true),
	}}
	for name, value := range api.containers {
		value.Config.Labels[labelEnvironment] = payload.EnvironmentID
		value.Config.Labels[labelComponent] = map[bool]string{true: "ANNOTATION", false: "EDITOR"}[name == payload.Components[0].ContainerName]
		value.NetworkSettings = &types.NetworkSettings{NetworkSettingsBase: types.NetworkSettingsBase{Ports: nat.PortMap{}}}
		api.containers[name] = value
	}

	if _, err := NewDockerExecutor(api, Validator{WorkspaceRoot: t.TempDir()}).StartPair(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if len(api.execContainers) != 1 || api.execContainers[0] != payload.Components[0].ContainerName {
		t.Fatalf("exec containers = %#v", api.execContainers)
	}
}

type notFoundError struct{ name string }

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (e notFoundError) Error() string { return "No such container: " + e.name }

func managedContainer(name, fingerprint string, running bool) types.ContainerJSON {
	return types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{Name: "/" + name, State: &types.ContainerState{Running: running}},
		Config: &dockercontainer.Config{Labels: map[string]string{
			labelManaged: "true", labelFingerprint: fingerprint,
		}},
	}
}
