package container

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"
	"github.com/opencontainers/image-spec/specs-go/v1"
	"xkp-agent/internal/protocol"
)

const (
	labelManaged     = "com.xkp.managed"
	labelEnvironment = "com.xkp.environment.id"
	labelComponent   = "com.xkp.component.type"
	labelFingerprint = "com.xkp.config.fingerprint"
)

type DockerAPI interface {
	ContainerInspect(context.Context, string) (types.ContainerJSON, error)
	ContainerCreate(context.Context, *dockercontainer.Config, *dockercontainer.HostConfig,
		*network.NetworkingConfig, *v1.Platform, string) (dockercontainer.CreateResponse, error)
	ContainerStart(context.Context, string, types.ContainerStartOptions) error
	ContainerStop(context.Context, string, dockercontainer.StopOptions) error
	ContainerRemove(context.Context, string, types.ContainerRemoveOptions) error
}

type DockerExecutor struct {
	mu        sync.Mutex
	api       DockerAPI
	validator Validator
}

func NewDockerExecutor(api DockerAPI, validator Validator) *DockerExecutor {
	return &DockerExecutor{api: api, validator: validator}
}

func (d *DockerExecutor) InspectPair(ctx context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.validator.ValidateControl(payload); err != nil {
		return PairResult{}, err
	}
	return d.inspectPair(ctx, payload, false)
}

func (d *DockerExecutor) CreatePair(ctx context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	workspace, err := d.validator.ValidateCreate(payload)
	if err != nil {
		return PairResult{}, err
	}
	return d.createPair(ctx, payload, workspace)
}

func (d *DockerExecutor) StartPair(ctx context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.validator.ValidateControl(payload); err != nil {
		return PairResult{}, err
	}
	states, err := d.inspectStates(ctx, payload, true)
	if err != nil {
		return PairResult{}, err
	}
	started := make([]string, 0, 2)
	for _, component := range payload.Components {
		if states[component.ContainerName] {
			continue
		}
		if err := d.api.ContainerStart(ctx, component.ContainerName, types.ContainerStartOptions{}); err != nil {
			d.compensateStop(ctx, started)
			return PairResult{}, fmt.Errorf("start component %s: %w", component.ComponentType, err)
		}
		started = append(started, component.ContainerName)
	}
	return runningPair(payload), nil
}

func (d *DockerExecutor) StopPair(ctx context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.validator.ValidateControl(payload); err != nil {
		return PairResult{}, err
	}
	states, err := d.inspectStates(ctx, payload, true)
	if err != nil {
		return PairResult{}, err
	}
	stopped := make([]string, 0, 2)
	timeout := 10
	for _, component := range payload.Components {
		if !states[component.ContainerName] {
			continue
		}
		if err := d.api.ContainerStop(ctx, component.ContainerName, dockercontainer.StopOptions{Timeout: &timeout}); err != nil {
			d.compensateStart(ctx, stopped)
			return PairResult{}, fmt.Errorf("stop component %s: %w", component.ComponentType, err)
		}
		stopped = append(stopped, component.ContainerName)
	}
	return stoppedPair(payload), nil
}

func (d *DockerExecutor) RestorePair(ctx context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	workspace, err := d.validator.ValidateCreate(payload)
	if err != nil {
		return PairResult{}, err
	}
	states, err := d.inspectStates(ctx, payload, false)
	if err != nil {
		return PairResult{}, err
	}
	timeout := 10
	for _, component := range payload.Components {
		if states[component.ContainerName] {
			if err := d.api.ContainerStop(ctx, component.ContainerName, dockercontainer.StopOptions{Timeout: &timeout}); err != nil {
				return PairResult{}, fmt.Errorf("stop component for restore %s: %w", component.ComponentType, err)
			}
		}
	}
	for _, component := range payload.Components {
		if _, exists, inspectErr := d.inspectComponent(ctx, payload.EnvironmentID, component); inspectErr != nil {
			return PairResult{}, inspectErr
		} else if exists {
			if removeErr := d.api.ContainerRemove(ctx, component.ContainerName,
				types.ContainerRemoveOptions{Force: false, RemoveVolumes: false}); removeErr != nil {
				return PairResult{}, fmt.Errorf("remove component %s: %w", component.ComponentType, removeErr)
			}
		}
	}
	return d.createPair(ctx, payload, workspace)
}

func (d *DockerExecutor) createPair(ctx context.Context, payload protocol.EnvironmentPayload,
	workspace string) (PairResult, error) {
	if err := os.MkdirAll(workspace, 0750); err != nil {
		return PairResult{}, fmt.Errorf("create shared workspace: %w", err)
	}
	missing := make([]protocol.EnvironmentComponent, 0, 2)
	for _, component := range payload.Components {
		_, exists, err := d.inspectComponent(ctx, payload.EnvironmentID, component)
		if err != nil {
			return PairResult{}, err
		}
		if !exists {
			missing = append(missing, component)
		}
	}
	created := make([]string, 0, len(missing))
	for _, component := range missing {
		config, hostConfig, err := dockerConfigs(payload.EnvironmentID, workspace, component)
		if err != nil {
			d.compensateRemove(ctx, created)
			return PairResult{}, err
		}
		if _, err := d.api.ContainerCreate(ctx, config, hostConfig, nil, nil, component.ContainerName); err != nil {
			d.compensateRemove(ctx, created)
			return PairResult{}, fmt.Errorf("create component %s: %w", component.ComponentType, err)
		}
		created = append(created, component.ContainerName)
	}
	return stoppedPair(payload), nil
}

func dockerConfigs(environmentID, workspace string, component protocol.EnvironmentComponent) (
	*dockercontainer.Config, *dockercontainer.HostConfig, error) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, value := range component.Ports {
		port, err := nat.NewPort(value.Protocol, strconv.Itoa(value.ContainerPort))
		if err != nil {
			return nil, nil, fmt.Errorf("build component port: %w", err)
		}
		exposed[port] = struct{}{}
		bindings[port] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(value.HostPort)}}
	}
	environment := []string{}
	deviceRequests := []dockercontainer.DeviceRequest(nil)
	if component.GPUEnabled {
		deviceRequests = []dockercontainer.DeviceRequest{{Driver: "nvidia", Count: -1, Capabilities: [][]string{{"gpu"}}}}
		environment = append(environment, fmt.Sprintf("XKP_GPU_COMPUTE_PERCENT=%d", component.GPUComputePercent))
		if component.GPUMemoryLimitBytes > 0 {
			environment = append(environment, fmt.Sprintf("XKP_GPU_MEMORY_LIMIT_BYTES=%d", component.GPUMemoryLimitBytes))
		}
	}
	config := &dockercontainer.Config{
		Image:        component.ImageReference,
		Cmd:          component.Command,
		WorkingDir:   component.WorkingDirectory,
		Env:          environment,
		ExposedPorts: exposed,
		Labels: map[string]string{
			labelManaged: "true", labelEnvironment: environmentID,
			labelComponent: component.ComponentType, labelFingerprint: component.ConfigFingerprint,
		},
	}
	hostConfig := &dockercontainer.HostConfig{
		Runtime:       component.RuntimeName,
		RestartPolicy: dockercontainer.RestartPolicy{Name: component.RestartPolicy},
		Binds:         []string{workspace + ":" + component.MountTarget + ":rw"},
		PortBindings:  bindings,
		Resources: dockercontainer.Resources{
			NanoCPUs:       int64(component.CPULimitMillis) * 1_000_000,
			Memory:         component.MemoryLimitBytes,
			DeviceRequests: deviceRequests,
		},
	}
	return config, hostConfig, nil
}

func (d *DockerExecutor) inspectStates(ctx context.Context, payload protocol.EnvironmentPayload,
	requireAll bool) (map[string]bool, error) {
	states := map[string]bool{}
	for _, component := range payload.Components {
		running, exists, err := d.inspectComponent(ctx, payload.EnvironmentID, component)
		if err != nil {
			return nil, err
		}
		if requireAll && !exists {
			return nil, fmt.Errorf("managed component is missing: %s", component.ContainerName)
		}
		states[component.ContainerName] = exists && running
	}
	return states, nil
}

func (d *DockerExecutor) inspectPair(ctx context.Context, payload protocol.EnvironmentPayload,
	requireAll bool) (PairResult, error) {
	result := PairResult{}
	for _, component := range payload.Components {
		running, exists, err := d.inspectComponent(ctx, payload.EnvironmentID, component)
		if err != nil {
			return PairResult{}, err
		}
		if requireAll && !exists {
			return PairResult{}, fmt.Errorf("managed component is missing: %s", component.ContainerName)
		}
		state := StateMissing
		if exists {
			state = StateStopped
			if running {
				state = StateRunning
			}
		}
		assignComponentResult(&result, component, state)
	}
	return result, nil
}

func (d *DockerExecutor) inspectComponent(ctx context.Context, environmentID string,
	component protocol.EnvironmentComponent) (bool, bool, error) {
	inspection, err := d.api.ContainerInspect(ctx, component.ContainerName)
	if err != nil {
		if errdefs.IsNotFound(err) || strings.Contains(err.Error(), "No such container") {
			return false, false, nil
		}
		return false, false, fmt.Errorf("inspect component %s: %w", component.ComponentType, err)
	}
	if inspection.Config == nil || inspection.Config.Labels[labelManaged] != "true" ||
		inspection.Config.Labels[labelEnvironment] != environmentID ||
		inspection.Config.Labels[labelComponent] != component.ComponentType ||
		inspection.Config.Labels[labelFingerprint] != component.ConfigFingerprint {
		return false, true, fmt.Errorf("managed container identity mismatch: %s", component.ContainerName)
	}
	running := inspection.State != nil && inspection.State.Running
	return running, true, nil
}

func (d *DockerExecutor) compensateRemove(ctx context.Context, names []string) {
	for index := len(names) - 1; index >= 0; index-- {
		_ = d.api.ContainerRemove(ctx, names[index], types.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
	}
}

func (d *DockerExecutor) compensateStop(ctx context.Context, names []string) {
	timeout := 10
	for index := len(names) - 1; index >= 0; index-- {
		_ = d.api.ContainerStop(ctx, names[index], dockercontainer.StopOptions{Timeout: &timeout})
	}
}

func (d *DockerExecutor) compensateStart(ctx context.Context, names []string) {
	for index := len(names) - 1; index >= 0; index-- {
		_ = d.api.ContainerStart(ctx, names[index], types.ContainerStartOptions{})
	}
}

func runningPair(payload protocol.EnvironmentPayload) PairResult {
	return pairWithState(payload, StateRunning)
}

func stoppedPair(payload protocol.EnvironmentPayload) PairResult {
	return pairWithState(payload, StateStopped)
}

func pairWithState(payload protocol.EnvironmentPayload, state State) PairResult {
	result := PairResult{}
	for _, component := range payload.Components {
		assignComponentResult(&result, component, state)
	}
	return result
}

func assignComponentResult(result *PairResult, component protocol.EnvironmentComponent, state State) {
	value := ComponentResult{ComponentType: component.ComponentType, ContainerName: component.ContainerName, State: state}
	if component.ComponentType == "ANNOTATION" {
		result.Annotation = value
	} else {
		result.Editor = value
	}
}
