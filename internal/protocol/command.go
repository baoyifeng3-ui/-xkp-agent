package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
)

const MaxCommandBytes = 4096

type CommandType string

const (
	ShutdownServer             CommandType = "SHUTDOWN_SERVER"
	CreateTrainingEnvironment  CommandType = "CREATE_TRAINING_ENVIRONMENT"
	StartTrainingEnvironment   CommandType = "START_TRAINING_ENVIRONMENT"
	StopTrainingEnvironment    CommandType = "STOP_TRAINING_ENVIRONMENT"
	RestoreTrainingEnvironment CommandType = "RESTORE_TRAINING_ENVIRONMENT"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type Command struct {
	CommandID      string              `json:"commandId"`
	Type           CommandType         `json:"type"`
	Version        int                 `json:"version"`
	LeaseToken     string              `json:"leaseToken"`
	LeaseExpiresAt time.Time           `json:"leaseExpiresAt"`
	Payload        json.RawMessage     `json:"payload"`
	Environment    *EnvironmentPayload `json:"-"`
}

type EnvironmentPayload struct {
	EnvironmentID         string                 `json:"environmentId"`
	OperationID           string                 `json:"operationId"`
	WorkspaceRelativePath string                 `json:"workspaceRelativePath,omitempty"`
	Components            []EnvironmentComponent `json:"components"`
}

type EnvironmentComponent struct {
	ComponentType       string            `json:"componentType"`
	ContainerName       string            `json:"containerName"`
	ConfigFingerprint   string            `json:"configFingerprint"`
	ImageReference      string            `json:"imageReference,omitempty"`
	RuntimeName         string            `json:"runtimeName,omitempty"`
	RestartPolicy       string            `json:"restartPolicy,omitempty"`
	MountTarget         string            `json:"mountTarget,omitempty"`
	Ports               []EnvironmentPort `json:"ports,omitempty"`
	Command             []string          `json:"command,omitempty"`
	WorkingDirectory    string            `json:"workingDirectory,omitempty"`
	CPULimitMillis      int               `json:"cpuLimitMillis,omitempty"`
	MemoryLimitBytes    int64             `json:"memoryLimitBytes,omitempty"`
	GPUEnabled          bool              `json:"gpuEnabled,omitempty"`
	GPUComputePercent   int               `json:"gpuComputePercent,omitempty"`
	GPUMemoryLimitBytes int64             `json:"gpuMemoryLimitBytes,omitempty"`
}

type EnvironmentPort struct {
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort"`
	Protocol      string `json:"protocol"`
}

type CommandResult struct {
	LeaseToken string                 `json:"leaseToken"`
	Success    bool                   `json:"success"`
	Code       string                 `json:"code"`
	Message    string                 `json:"message,omitempty"`
	Details    map[string]interface{} `json:"details,omitempty"`
}

func DecodeCommand(data []byte) (Command, error) {
	if len(data) == 0 || len(data) > MaxCommandBytes {
		return Command{}, fmt.Errorf("command envelope size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var command Command
	if err := decoder.Decode(&command); err != nil {
		return Command{}, fmt.Errorf("decode command envelope: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return Command{}, err
	}
	if !uuidPattern.MatchString(command.CommandID) || !uuidPattern.MatchString(command.LeaseToken) {
		return Command{}, fmt.Errorf("command identifiers are invalid")
	}
	if command.Version != 1 {
		return Command{}, fmt.Errorf("command type or version is unsupported")
	}
	if command.LeaseExpiresAt.IsZero() {
		return Command{}, fmt.Errorf("command lease expiry is required")
	}
	if command.Type == ShutdownServer {
		var payload map[string]json.RawMessage
		if len(command.Payload) == 0 || json.Unmarshal(command.Payload, &payload) != nil || payload == nil || len(payload) != 0 {
			return Command{}, fmt.Errorf("shutdown payload must be an empty object")
		}
		return command, nil
	}
	if command.Type != CreateTrainingEnvironment && command.Type != RestoreTrainingEnvironment &&
		command.Type != StartTrainingEnvironment && command.Type != StopTrainingEnvironment {
		return Command{}, fmt.Errorf("command type or version is unsupported")
	}
	var environment EnvironmentPayload
	if err := decodeStrict(command.Payload, &environment); err != nil {
		return Command{}, fmt.Errorf("decode environment payload: %w", err)
	}
	if err := validateEnvironment(command.Type, environment); err != nil {
		return Command{}, err
	}
	command.Environment = &environment
	return command, nil
}

func decodeStrict(data []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureJSONEnd(decoder)
}

func validateEnvironment(commandType CommandType, payload EnvironmentPayload) error {
	if !uuidPattern.MatchString(payload.EnvironmentID) || !uuidPattern.MatchString(payload.OperationID) {
		return fmt.Errorf("environment identifiers are invalid")
	}
	full := commandType == CreateTrainingEnvironment || commandType == RestoreTrainingEnvironment
	if full {
		if !validRelativeWorkspace(payload.WorkspaceRelativePath) {
			return fmt.Errorf("workspace path is invalid")
		}
	} else if payload.WorkspaceRelativePath != "" {
		return fmt.Errorf("workspace is not allowed for this command")
	}
	if len(payload.Components) != 2 {
		return fmt.Errorf("environment must contain two components")
	}
	seenTypes := map[string]bool{}
	seenHosts := map[string]bool{}
	for _, component := range payload.Components {
		if component.ComponentType != "ANNOTATION" && component.ComponentType != "EDITOR" {
			return fmt.Errorf("component type is invalid")
		}
		if seenTypes[component.ComponentType] {
			return fmt.Errorf("component type is duplicated")
		}
		seenTypes[component.ComponentType] = true
		if !validContainerName(component.ContainerName) || !validFingerprint(component.ConfigFingerprint) {
			return fmt.Errorf("component identity is invalid")
		}
		if full {
			if component.ImageReference == "" || component.RestartPolicy != "always" || component.CPULimitMillis < 100 || component.CPULimitMillis > 128000 || component.MemoryLimitBytes < 128*1024*1024 || component.MemoryLimitBytes > 512*1024*1024*1024 {
				return fmt.Errorf("component resources are invalid")
			}
			if component.ComponentType == "ANNOTATION" && (component.RuntimeName != "sysbox-runc" || component.MountTarget != "/root/data") {
				return fmt.Errorf("annotation component configuration is invalid")
			}
			if component.ComponentType == "EDITOR" && (component.RuntimeName != "nvidia" || component.MountTarget != "/home/student/data") {
				return fmt.Errorf("editor component configuration is invalid")
			}
			for _, port := range component.Ports {
				if port.ContainerPort < 1 || port.ContainerPort > 65535 || port.HostPort < 1 || port.HostPort > 65535 || (port.Protocol != "tcp" && port.Protocol != "udp") {
					return fmt.Errorf("component port is invalid")
				}
				key := fmt.Sprintf("%d/%s", port.HostPort, port.Protocol)
				if seenHosts[key] {
					return fmt.Errorf("host port is duplicated")
				}
				seenHosts[key] = true
			}
			if component.GPUEnabled && (component.GPUComputePercent < 1 || component.GPUComputePercent > 100) {
				return fmt.Errorf("GPU limit is invalid")
			}
		} else if len(component.Ports) != 0 || component.ImageReference != "" || component.RuntimeName != "" || component.MountTarget != "" {
			return fmt.Errorf("start or stop contains create fields")
		}
	}
	if !seenTypes["ANNOTATION"] || !seenTypes["EDITOR"] {
		return fmt.Errorf("annotation and editor components are required")
	}
	return nil
}

func validRelativeWorkspace(value string) bool {
	return value != "" && !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "\\") &&
		path.Clean(value) == value && value != "." && value != ".." && !strings.Contains(value, ":")
}

func validContainerName(value string) bool {
	return len(value) >= 3 && len(value) <= 128 && !strings.ContainsAny(value, " /\\")
}
func validFingerprint(value string) bool {
	return len(value) == 64 && regexp.MustCompile(`^[0-9a-fA-F]+$`).MatchString(value)
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing interface{}
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("command envelope contains trailing JSON")
	}
	return fmt.Errorf("command envelope trailing data is invalid")
}
