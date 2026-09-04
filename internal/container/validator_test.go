package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xkp-agent/internal/protocol"
)

func TestValidatorAcceptsAValidPairInsideWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	validator := Validator{WorkspaceRoot: root}
	payload := validPayload()

	workspace, err := validator.ValidateCreate(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "training", "21", "31")
	if workspace != want {
		t.Fatalf("workspace = %q, want %q", workspace, want)
	}
}

func TestValidatorAcceptsUnlimitedCPUAndMemory(t *testing.T) {
	payload := validPayload()
	payload.Components[0].CPULimitMillis = 0
	payload.Components[0].MemoryLimitBytes = 0
	if _, err := (Validator{WorkspaceRoot: t.TempDir()}).ValidateCreate(payload); err != nil {
		t.Fatalf("unlimited component rejected: %v", err)
	}
}

func TestValidatorAcceptsBoundedEditorBootstrapCommand(t *testing.T) {
	payload := validPayload()
	payload.Components[1].Command = []string{"/bin/sh", "-c", strings.Repeat("x", 1187)}
	if _, err := (Validator{WorkspaceRoot: t.TempDir()}).ValidateCreate(payload); err != nil {
		t.Fatalf("bounded bootstrap command rejected: %v", err)
	}
}

func TestValidatorRejectsCommandArgumentAboveEnvelopeBudget(t *testing.T) {
	payload := validPayload()
	payload.Components[1].Command = []string{"/bin/sh", "-c", strings.Repeat("x", 2049)}
	if _, err := (Validator{WorkspaceRoot: t.TempDir()}).ValidateCreate(payload); err == nil {
		t.Fatal("oversized command argument was accepted")
	}
}

func TestValidatorRejectsUnsafeCreateSpecifications(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name   string
		change func(*protocol.EnvironmentPayload)
	}{
		{"workspace traversal", func(p *protocol.EnvironmentPayload) { p.WorkspaceRelativePath = "../etc" }},
		{"invalid fingerprint", func(p *protocol.EnvironmentPayload) { p.Components[0].ConfigFingerprint = "bad" }},
		{"invalid name", func(p *protocol.EnvironmentPayload) { p.Components[0].ContainerName = "bad name" }},
		{"invalid image", func(p *protocol.EnvironmentPayload) { p.Components[0].ImageReference = "image with spaces" }},
		{"wrong annotation runtime", func(p *protocol.EnvironmentPayload) { p.Components[0].RuntimeName = "runc" }},
		{"wrong editor mount", func(p *protocol.EnvironmentPayload) { p.Components[1].MountTarget = "/tmp" }},
		{"duplicate host port", func(p *protocol.EnvironmentPayload) { p.Components[1].Ports[0].HostPort = 8081 }},
		{"cpu too small", func(p *protocol.EnvironmentPayload) { p.Components[0].CPULimitMillis = 99 }},
		{"memory too small", func(p *protocol.EnvironmentPayload) { p.Components[0].MemoryLimitBytes = 1024 }},
		{"gpu percentage too large", func(p *protocol.EnvironmentPayload) { p.Components[1].GPUComputePercent = 101 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := validPayload()
			test.change(&payload)
			if _, err := (Validator{WorkspaceRoot: root}).ValidateCreate(payload); err == nil {
				t.Fatal("unsafe specification was accepted")
			}
		})
	}
}

func TestValidatorRejectsSymlinkedWorkspaceParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "training")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := (Validator{WorkspaceRoot: root}).ValidateCreate(validPayload()); err == nil {
		t.Fatal("symlinked workspace parent was accepted")
	}
}

func validPayload() protocol.EnvironmentPayload {
	return protocol.EnvironmentPayload{
		EnvironmentID:         "11111111-2222-4333-8444-555555555555",
		OperationID:           "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		WorkspaceRelativePath: "training/21/31",
		Components: []protocol.EnvironmentComponent{
			{
				ComponentType: "ANNOTATION", ContainerName: "xkp-train-11111111-annotation",
				ConfigFingerprint: strings.Repeat("a", 64), ImageReference: "xkp/annotation:v1",
				RuntimeName: "sysbox-runc", RestartPolicy: "always", MountTarget: "/root/data",
				Ports:          []protocol.EnvironmentPort{{ContainerPort: 8080, HostPort: 8081, Protocol: "tcp"}},
				CPULimitMillis: 1000, MemoryLimitBytes: 1024 * 1024 * 1024,
			},
			{
				ComponentType: "EDITOR", ContainerName: "xkp-train-11111111-editor",
				ConfigFingerprint: strings.Repeat("b", 64), ImageReference: "xkp/editor:v1",
				RuntimeName: "nvidia", RestartPolicy: "always", MountTarget: "/home/student/data",
				Ports:          []protocol.EnvironmentPort{{ContainerPort: 9090, HostPort: 9091, Protocol: "tcp"}},
				CPULimitMillis: 2000, MemoryLimitBytes: 6 * 1024 * 1024 * 1024,
				GPUEnabled: true, GPUComputePercent: 50,
			},
		},
	}
}
