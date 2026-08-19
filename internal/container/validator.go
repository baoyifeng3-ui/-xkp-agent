package container

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"xkp-agent/internal/protocol"
)

var (
	containerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{2,127}$`)
	fingerprintPattern   = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	imagePattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$`)
)

type Validator struct {
	WorkspaceRoot string
}

func (v Validator) ValidateCreate(payload protocol.EnvironmentPayload) (string, error) {
	if err := validatePairIdentity(payload); err != nil {
		return "", err
	}
	workspace, err := v.resolveWorkspace(payload.WorkspaceRelativePath)
	if err != nil {
		return "", err
	}
	seenPorts := map[string]bool{}
	for _, component := range payload.Components {
		if !imagePattern.MatchString(component.ImageReference) || component.RestartPolicy != "always" {
			return "", fmt.Errorf("component image or restart policy is invalid")
		}
		if component.CPULimitMillis < 100 || component.CPULimitMillis > 128000 ||
			component.MemoryLimitBytes < 128*1024*1024 || component.MemoryLimitBytes > 512*1024*1024*1024 {
			return "", fmt.Errorf("component resources are invalid")
		}
		if component.ComponentType == "ANNOTATION" {
			if component.RuntimeName != "sysbox-runc" || component.MountTarget != "/root/data" || component.GPUEnabled {
				return "", fmt.Errorf("annotation component configuration is invalid")
			}
		} else if component.RuntimeName != "nvidia" || component.MountTarget != "/home/student/data" {
			return "", fmt.Errorf("editor component configuration is invalid")
		}
		if component.GPUEnabled && (component.GPUComputePercent < 1 || component.GPUComputePercent > 100) {
			return "", fmt.Errorf("GPU limit is invalid")
		}
		if len(component.Ports) == 0 {
			return "", fmt.Errorf("component ports are required")
		}
		for _, port := range component.Ports {
			if port.ContainerPort < 1 || port.ContainerPort > 65535 || port.HostPort < 1 || port.HostPort > 65535 ||
				(port.Protocol != "tcp" && port.Protocol != "udp") {
				return "", fmt.Errorf("component port is invalid")
			}
			key := fmt.Sprintf("%d/%s", port.HostPort, port.Protocol)
			if seenPorts[key] {
				return "", fmt.Errorf("host port is duplicated")
			}
			seenPorts[key] = true
		}
		if component.WorkingDirectory != "" &&
			(!strings.HasPrefix(component.WorkingDirectory, "/") || strings.Contains(component.WorkingDirectory, "..")) {
			return "", fmt.Errorf("working directory is invalid")
		}
		for _, argument := range component.Command {
			if argument == "" || len(argument) > 1024 || strings.ContainsRune(argument, '\x00') {
				return "", fmt.Errorf("component command is invalid")
			}
		}
	}
	return workspace, nil
}

func (v Validator) ValidateControl(payload protocol.EnvironmentPayload) error {
	return validatePairIdentity(payload)
}

func validatePairIdentity(payload protocol.EnvironmentPayload) error {
	if payload.EnvironmentID == "" || payload.OperationID == "" || len(payload.Components) != 2 {
		return fmt.Errorf("environment identity is invalid")
	}
	seenTypes := map[string]bool{}
	seenNames := map[string]bool{}
	for _, component := range payload.Components {
		if component.ComponentType != "ANNOTATION" && component.ComponentType != "EDITOR" {
			return fmt.Errorf("component type is invalid")
		}
		if seenTypes[component.ComponentType] || seenNames[component.ContainerName] {
			return fmt.Errorf("component identity is duplicated")
		}
		if !containerNamePattern.MatchString(component.ContainerName) || !fingerprintPattern.MatchString(component.ConfigFingerprint) {
			return fmt.Errorf("component identity is invalid")
		}
		seenTypes[component.ComponentType] = true
		seenNames[component.ContainerName] = true
	}
	if !seenTypes["ANNOTATION"] || !seenTypes["EDITOR"] {
		return fmt.Errorf("annotation and editor components are required")
	}
	return nil
}

func (v Validator) resolveWorkspace(relative string) (string, error) {
	if v.WorkspaceRoot == "" || !filepath.IsAbs(v.WorkspaceRoot) || relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("workspace path is invalid")
	}
	cleanRelative := filepath.Clean(filepath.FromSlash(relative))
	if cleanRelative == "." || cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace path is invalid")
	}
	root, err := filepath.Abs(filepath.Clean(v.WorkspaceRoot))
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	workspace := filepath.Join(root, cleanRelative)
	rel, err := filepath.Rel(root, workspace)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace escapes configured root")
	}
	current := root
	for _, segment := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			break
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect workspace path: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("workspace path contains a symlink")
		}
	}
	return workspace, nil
}
