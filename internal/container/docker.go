package container

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

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
	ContainerExecCreate(context.Context, string, types.ExecConfig) (types.IDResponse, error)
	ContainerExecStart(context.Context, string, types.ExecStartCheck) error
	ContainerExecInspect(context.Context, string) (types.ContainerExecInspect, error)
}

type DockerExecutor struct {
	mu        sync.Mutex
	api       DockerAPI
	validator Validator
	mps       MPSManager
}

func NewDockerExecutor(api DockerAPI, validator Validator, managers ...MPSManager) *DockerExecutor {
	var manager MPSManager
	if len(managers) > 0 {
		manager = managers[0]
	}
	return &DockerExecutor{api: api, validator: validator, mps: manager}
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
	mpsDirectory, err := d.ensureMPS(ctx, payload)
	if err != nil {
		return PairResult{}, err
	}
	return d.createPair(ctx, payload, workspace, mpsDirectory)
}

func (d *DockerExecutor) StartPair(ctx context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	if err := d.validator.ValidateControl(payload); err != nil {
		return PairResult{}, err
	}
	if _, err := d.ensureMPS(ctx, payload); err != nil {
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
	for _, component := range payload.Components {
		if component.ComponentType == "EDITOR" && component.MPSEnabled {
			if err := d.installCondaMPSCompatibility(ctx, component.ContainerName); err != nil {
				d.compensateStop(ctx, started)
				return PairResult{}, err
			}
		}
	}
	for _, component := range payload.Components {
		if err := d.waitReady(ctx, component); err != nil {
			d.compensateStop(ctx, started)
			return PairResult{}, err
		}
		if component.ComponentType == "ANNOTATION" {
			if err := d.initializeAnnotationAuth(ctx, component.ContainerName); err != nil {
				d.compensateStop(ctx, started)
				return PairResult{}, err
			}
		}
	}
	return runningPair(payload), nil
}

func (d *DockerExecutor) installCondaMPSCompatibility(ctx context.Context, containerName string) error {
	command := `test ! -x /root/anaconda3/bin/conda||{ p=/usr/local/bin/xkp-conda-safe;printf '%s\n' '#!/bin/sh' 'unset CUDA_MPS_ACTIVE_THREAD_PERCENTAGE CUDA_MPS_PIPE_DIRECTORY CUDA_MPS_LOG_DIRECTORY CUDA_MPS_PINNED_DEVICE_MEM_LIMIT' 'exec /root/anaconda3/bin/conda "$@"' >$p;chmod 755 $p;grep -q '^# XKP_CONDA_MPS_SAFE_V2$' /root/.bashrc 2>/dev/null||cat >>/root/.bashrc <<'XKP'
# XKP_CONDA_MPS_SAFE_V2
export CONDA_EXE=/usr/local/bin/xkp-conda-safe
if declare -f conda >/dev/null;then
  eval "$(declare -f conda|sed '1s/^conda /xkp_conda_raw /')"
  xkp_conda(){ local a="${CUDA_MPS_ACTIVE_THREAD_PERCENTAGE-}" p="${CUDA_MPS_PIPE_DIRECTORY-}" l="${CUDA_MPS_LOG_DIRECTORY-}" m="${CUDA_MPS_PINNED_DEVICE_MEM_LIMIT-}";unset CUDA_MPS_ACTIVE_THREAD_PERCENTAGE CUDA_MPS_PIPE_DIRECTORY CUDA_MPS_LOG_DIRECTORY CUDA_MPS_PINNED_DEVICE_MEM_LIMIT;xkp_conda_raw "$@";local r=$?;[ -z "$a" ]||export CUDA_MPS_ACTIVE_THREAD_PERCENTAGE="$a";[ -z "$p" ]||export CUDA_MPS_PIPE_DIRECTORY="$p";[ -z "$l" ]||export CUDA_MPS_LOG_DIRECTORY="$l";[ -z "$m" ]||export CUDA_MPS_PINNED_DEVICE_MEM_LIMIT="$m";return "$r";}
  alias conda=xkp_conda
fi
XKP
}`
	created, err := d.api.ContainerExecCreate(ctx, containerName, types.ExecConfig{Cmd: []string{"sh", "-lc", command}})
	if err != nil {
		return fmt.Errorf("prepare Conda MPS compatibility: %w", err)
	}
	if err = d.api.ContainerExecStart(ctx, created.ID, types.ExecStartCheck{Detach: true}); err != nil {
		return fmt.Errorf("start Conda MPS compatibility: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, inspectErr := d.api.ContainerExecInspect(ctx, created.ID)
		if inspectErr != nil {
			return fmt.Errorf("inspect Conda MPS compatibility: %w", inspectErr)
		}
		if !status.Running {
			if status.ExitCode != 0 {
				return fmt.Errorf("Conda MPS compatibility initialization failed")
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Conda MPS compatibility initialization timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (d *DockerExecutor) initializeAnnotationAuth(ctx context.Context, containerName string) error {
	command := `t=$(docker exec cvat python3 /home/django/manage.py shell -c "from rest_framework.authtoken.models import Token; print(Token.objects.first().key)"|tail -1)&&test -n "$t"&&docker exec cvat_proxy sh -c "sed -i '/proxy_set_header Authorization/d' /etc/nginx/conf.d/default.conf;sed -i '/proxy_set_header        Host/a\\    proxy_set_header Authorization \"Token $t\";' /etc/nginx/conf.d/default.conf;nginx -s reload"`
	created, err := d.api.ContainerExecCreate(ctx, containerName, types.ExecConfig{Cmd: []string{"sh", "-lc", command}})
	if err != nil {
		return fmt.Errorf("prepare annotation authentication: %w", err)
	}
	if err = d.api.ContainerExecStart(ctx, created.ID, types.ExecStartCheck{Detach: true}); err != nil {
		return fmt.Errorf("start annotation authentication: %w", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		status, inspectErr := d.api.ContainerExecInspect(ctx, created.ID)
		if inspectErr != nil {
			return fmt.Errorf("inspect annotation authentication: %w", inspectErr)
		}
		if !status.Running {
			if status.ExitCode != 0 {
				return fmt.Errorf("annotation authentication initialization failed")
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("annotation authentication initialization timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (d *DockerExecutor) waitReady(ctx context.Context, component protocol.EnvironmentComponent) error {
	inspection, err := d.api.ContainerInspect(ctx, component.ContainerName)
	if err != nil || inspection.NetworkSettings == nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	deadline := time.Now().Add(40 * time.Second)
	for _, port := range []int{8080, 9090, 8888, 5000} {
		if (component.ComponentType == "ANNOTATION") != (port == 8080) {
			continue
		}
		bindings := inspection.NetworkSettings.Ports[nat.Port(fmt.Sprintf("%d/tcp", port))]
		if len(bindings) == 0 {
			continue
		}
		scheme, path := "http", "/"
		if port == 9090 {
			scheme = "https"
		}
		if port == 8888 {
			path = "/lab"
		}
		url := fmt.Sprintf("%s://127.0.0.1:%s%s", scheme, bindings[0].HostPort, path)
		for {
			if port == 8080 || port == 5000 {
				connection, dialErr := net.DialTimeout("tcp", "127.0.0.1:"+bindings[0].HostPort, time.Second)
				if dialErr == nil {
					connection.Close()
					break
				}
			} else {
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				response, requestErr := client.Do(request)
				if requestErr == nil {
					io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if response.StatusCode < 500 {
						break
					}
				}
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("component service is not ready: %s:%d", component.ComponentType, port)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return nil
}

func (d *DockerExecutor) StopPair(ctx context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
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
	mpsDirectory, err := d.ensureMPS(ctx, payload)
	if err != nil {
		return PairResult{}, err
	}
	states := map[string]bool{}
	for _, component := range payload.Components {
		running, _, inspectErr := d.inspectOwnedComponent(ctx, payload.EnvironmentID, component)
		if inspectErr != nil {
			return PairResult{}, inspectErr
		}
		states[component.ContainerName] = running
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
		if _, exists, inspectErr := d.inspectOwnedComponent(ctx, payload.EnvironmentID, component); inspectErr != nil {
			return PairResult{}, inspectErr
		} else if exists {
			if removeErr := d.api.ContainerRemove(ctx, component.ContainerName,
				types.ContainerRemoveOptions{Force: false, RemoveVolumes: false}); removeErr != nil {
				return PairResult{}, fmt.Errorf("remove component %s: %w", component.ComponentType, removeErr)
			}
		}
	}
	return d.createPair(ctx, payload, workspace, mpsDirectory)
}

func (d *DockerExecutor) DeletePair(ctx context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.validator.ValidateControl(payload); err != nil {
		return PairResult{}, err
	}
	timeout := 10
	for _, component := range payload.Components {
		running, exists, err := d.inspectOwnedComponent(ctx, payload.EnvironmentID, component)
		if err != nil {
			return PairResult{}, err
		}
		if !exists {
			continue
		}
		if running {
			if err := d.api.ContainerStop(ctx, component.ContainerName,
				dockercontainer.StopOptions{Timeout: &timeout}); err != nil {
				return PairResult{}, fmt.Errorf("stop component for delete %s: %w", component.ComponentType, err)
			}
		}
		if err := d.api.ContainerRemove(ctx, component.ContainerName,
			types.ContainerRemoveOptions{Force: false, RemoveVolumes: false}); err != nil {
			return PairResult{}, fmt.Errorf("remove component %s: %w", component.ComponentType, err)
		}
	}
	return pairWithState(payload, StateMissing), nil
}

func (d *DockerExecutor) createPair(ctx context.Context, payload protocol.EnvironmentPayload,
	workspace string, mpsDirectory string) (PairResult, error) {
	if err := os.MkdirAll(workspace, 0750); err != nil {
		return PairResult{}, fmt.Errorf("create shared workspace: %w", err)
	}
	if err := os.Chmod(workspace, 0755); err != nil {
		return PairResult{}, fmt.Errorf("set shared workspace permissions: %w", err)
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
		config, hostConfig, err := dockerConfigs(payload.EnvironmentID, workspace, component, mpsDirectory, d.validator.CodeServerTLSDir)
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

func dockerConfigs(environmentID, workspace string, component protocol.EnvironmentComponent, mpsDirectory, codeServerTLSDir string) (
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
		limited := component.MPSEnabled || component.GPUComputePercent > 0 || component.GPUMemoryLimitBytes > 0
		if limited {
			environment = append(environment, "TF_FORCE_GPU_ALLOW_GROWTH=true")
			deviceRequests = []dockercontainer.DeviceRequest{{Driver: "nvidia", Count: -1, Capabilities: [][]string{{"gpu"}}}}
		}
		if component.GPUComputePercent > 0 {
			environment = append(environment, fmt.Sprintf("XKP_GPU_COMPUTE_PERCENT=%d", component.GPUComputePercent))
			if component.MPSEnabled {
				environment = append(environment, fmt.Sprintf("CUDA_MPS_ACTIVE_THREAD_PERCENTAGE=%d", component.GPUComputePercent))
			}
		}
		if component.GPUMemoryLimitBytes > 0 {
			environment = append(environment, fmt.Sprintf("XKP_GPU_MEMORY_LIMIT_BYTES=%d", component.GPUMemoryLimitBytes))
			if component.MPSEnabled {
				environment = append(environment, fmt.Sprintf("CUDA_MPS_PINNED_DEVICE_MEM_LIMIT=0=%dM",
					component.GPUMemoryLimitBytes/(1024*1024)))
			}
		}
		if component.MPSEnabled && mpsDirectory != "" {
			environment = append(environment, "CUDA_MPS_PIPE_DIRECTORY="+mpsDirectory)
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
	if component.MPSEnabled && mpsDirectory != "" {
		hostConfig.Binds = append(hostConfig.Binds, mpsDirectory+":"+mpsDirectory+":rw")
	}
	if component.ComponentType == "EDITOR" {
		if codeServerTLSDir == "" {
			codeServerTLSDir = "/etc/xkp-agent/code-server-tls"
		}
		hostConfig.Binds = append(hostConfig.Binds,
			codeServerTLSDir+"/code-cert.pem:/root/.config/code-cert.pem:ro",
			codeServerTLSDir+"/code-cert-key.pem:/root/.config/code-cert-key.pem:ro")
	}
	return config, hostConfig, nil
}

func (d *DockerExecutor) ensureMPS(ctx context.Context, payload protocol.EnvironmentPayload) (string, error) {
	for _, component := range payload.Components {
		if component.MPSEnabled {
			if d.mps == nil {
				return "", fmt.Errorf("MPS manager is not configured")
			}
			return d.mps.EnsureRunning(ctx)
		}
	}
	return "", nil
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

func (d *DockerExecutor) inspectOwnedComponent(ctx context.Context, environmentID string,
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
		inspection.Config.Labels[labelComponent] != component.ComponentType {
		return false, true, fmt.Errorf("managed container identity mismatch: %s", component.ContainerName)
	}
	return inspection.State != nil && inspection.State.Running, true, nil
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
	value := ComponentResult{ComponentType: component.ComponentType, ContainerName: component.ContainerName,
		ConfigFingerprint: component.ConfigFingerprint, State: state}
	if component.ComponentType == "ANNOTATION" {
		result.Annotation = value
	} else {
		result.Editor = value
	}
}
