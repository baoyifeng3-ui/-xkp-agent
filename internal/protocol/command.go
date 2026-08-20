package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

type TerminalTicket struct {
	Ticket    string
	ExpiresAt time.Time
}

const MaxCommandBytes = 4096

type CommandType string

const (
	ShutdownServer             CommandType = "SHUTDOWN_SERVER"
	CreateTrainingEnvironment  CommandType = "CREATE_TRAINING_ENVIRONMENT"
	StartTrainingEnvironment   CommandType = "START_TRAINING_ENVIRONMENT"
	StopTrainingEnvironment    CommandType = "STOP_TRAINING_ENVIRONMENT"
	RestoreTrainingEnvironment CommandType = "RESTORE_TRAINING_ENVIRONMENT"
	OpenRootTerminal           CommandType = "OPEN_ROOT_TERMINAL"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
var canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var canonicalTimePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

type Command struct {
	CommandID      string              `json:"commandId"`
	Type           CommandType         `json:"type"`
	Version        int                 `json:"version"`
	LeaseToken     string              `json:"leaseToken"`
	LeaseExpiresAt time.Time           `json:"leaseExpiresAt"`
	Payload        json.RawMessage     `json:"payload"`
	Environment    *EnvironmentPayload `json:"-"`
	Terminal       *TerminalPayload    `json:"-"`
}

type TerminalPayload struct {
	SessionID               string    `json:"sessionId"`
	RelayURL                string    `json:"relayUrl"`
	AgentConnectionDeadline time.Time `json:"agentConnectionDeadline"`
	IdleTimeoutSeconds      int       `json:"idleTimeoutSeconds"`
	AbsoluteExpiresAt       time.Time `json:"absoluteExpiresAt"`
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
	return DecodeCommandAt(data, time.Now().UTC(), false)
}

func DecodeCommandAt(data []byte, now time.Time, allowInsecureLoopback bool) (Command, error) {
	if len(data) == 0 || len(data) > MaxCommandBytes {
		return Command{}, fmt.Errorf("command envelope size is invalid")
	}
	if err := rejectDuplicateFields(data); err != nil {
		return Command{}, fmt.Errorf("decode command envelope: %w", err)
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
	identifierPattern := uuidPattern
	if command.Type == OpenRootTerminal {
		identifierPattern = canonicalUUIDPattern
	}
	if !identifierPattern.MatchString(command.CommandID) || !identifierPattern.MatchString(command.LeaseToken) {
		return Command{}, fmt.Errorf("command identifiers are invalid")
	}
	if command.Version != 1 {
		return Command{}, fmt.Errorf("command type or version is unsupported")
	}
	if command.LeaseExpiresAt.IsZero() {
		return Command{}, fmt.Errorf("command lease expiry is required")
	}
	if command.Type == OpenRootTerminal {
		var rawEnvelope struct {
			LeaseExpiresAt json.RawMessage `json:"leaseExpiresAt"`
		}
		if err := json.Unmarshal(data, &rawEnvelope); err != nil ||
			!isCanonicalTime(rawEnvelope.LeaseExpiresAt, command.LeaseExpiresAt) {
			return Command{}, fmt.Errorf("command lease expiry is invalid")
		}
	}
	if command.Type == ShutdownServer {
		var payload map[string]json.RawMessage
		if len(command.Payload) == 0 || json.Unmarshal(command.Payload, &payload) != nil || payload == nil || len(payload) != 0 {
			return Command{}, fmt.Errorf("shutdown payload must be an empty object")
		}
		return command, nil
	}
	if command.Type == OpenRootTerminal {
		var terminal TerminalPayload
		if err := decodeStrict(command.Payload, &terminal); err != nil {
			return Command{}, fmt.Errorf("decode terminal payload: %w", err)
		}
		if err := validateTerminal(command.Payload, terminal, now, allowInsecureLoopback); err != nil {
			return Command{}, err
		}
		command.Terminal = &terminal
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

func validateTerminal(raw json.RawMessage, payload TerminalPayload, now time.Time, allowInsecureLoopback bool) error {
	if !canonicalUUIDPattern.MatchString(payload.SessionID) || payload.IdleTimeoutSeconds != 600 {
		return fmt.Errorf("terminal payload identity or idle timeout is invalid")
	}
	var rawTimes struct {
		AgentConnectionDeadline json.RawMessage `json:"agentConnectionDeadline"`
		AbsoluteExpiresAt       json.RawMessage `json:"absoluteExpiresAt"`
	}
	if err := json.Unmarshal(raw, &rawTimes); err != nil ||
		!isCanonicalTime(rawTimes.AgentConnectionDeadline, payload.AgentConnectionDeadline) ||
		!isCanonicalTime(rawTimes.AbsoluteExpiresAt, payload.AbsoluteExpiresAt) {
		return fmt.Errorf("terminal deadlines are invalid")
	}
	deadline := payload.AgentConnectionDeadline
	absExpiry := payload.AbsoluteExpiresAt
	if !now.Before(deadline) || !absExpiry.After(deadline) || absExpiry.After(deadline.Add(2*time.Hour)) {
		return fmt.Errorf("terminal deadlines are invalid")
	}
	if err := validateTerminalRelayURL(payload.RelayURL, payload.SessionID, allowInsecureLoopback); err != nil {
		return err
	}
	return nil
}

func validateTerminalRelayURL(value, sessionID string, allowInsecureLoopback bool) error {
	relay, err := url.Parse(value)
	if err != nil || relay.Opaque != "" || relay.Hostname() == "" || relay.User != nil ||
		relay.RawQuery != "" || relay.ForceQuery || relay.Fragment != "" {
		return fmt.Errorf("terminal relay URL is invalid")
	}
	if relay.Scheme != "wss" {
		if relay.Scheme != "ws" || !allowInsecureLoopback || !isExactLoopback(relay.Hostname()) {
			return fmt.Errorf("terminal relay URL is invalid")
		}
	}
	expectedPath := "/terminal/v1/agent/" + sessionID
	if relay.Path != expectedPath || relay.EscapedPath() != expectedPath {
		return fmt.Errorf("terminal relay URL is invalid")
	}
	return nil
}

func isExactLoopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func isCanonicalTime(raw json.RawMessage, parsed time.Time) bool {
	if parsed.IsZero() || parsed.Location() != time.UTC {
		return false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !canonicalTimePattern.MatchString(value) {
		return false
	}
	return value == parsed.Format("2006-01-02T15:04:05Z")
}

func decodeStrict(data []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureJSONEnd(decoder)
}

func rejectDuplicateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object field name is invalid")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("command envelope contains trailing JSON")
		}
		return err
	}
	return nil
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
