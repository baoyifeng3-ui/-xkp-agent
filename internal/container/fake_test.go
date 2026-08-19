package container

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFakeExecutorConvergesPairAndPreservesWorkspaceOnRestore(t *testing.T) {
	root := t.TempDir()
	executor := NewFakeExecutor(Validator{WorkspaceRoot: root})
	payload := validPayload()

	created, err := executor.CreatePair(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if created.Annotation.State != StateStopped || created.Editor.State != StateStopped {
		t.Fatalf("created = %#v", created)
	}
	sentinel := filepath.Join(root, "training", "21", "31", "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	started, err := executor.StartPair(context.Background(), payload)
	if err != nil || started.Annotation.State != StateRunning || started.Editor.State != StateRunning {
		t.Fatalf("started = %#v, err = %v", started, err)
	}
	if _, err := executor.StopPair(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	restored, err := executor.RestorePair(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Annotation.State != StateStopped || restored.Editor.State != StateStopped {
		t.Fatalf("restored = %#v", restored)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("sentinel data = %q, err = %v", data, err)
	}
}

func TestFakeExecutorDoesNotCreateHalfAPairOnIdentityConflict(t *testing.T) {
	executor := NewFakeExecutor(Validator{WorkspaceRoot: t.TempDir()})
	payload := validPayload()
	executor.containers[payload.Components[1].ContainerName] = fakeContainer{
		Fingerprint: "different", State: StateStopped,
	}

	if _, err := executor.CreatePair(context.Background(), payload); err == nil {
		t.Fatal("expected identity conflict")
	}
	if _, exists := executor.containers[payload.Components[0].ContainerName]; exists {
		t.Fatal("annotation container was left behind after pair failure")
	}
}
