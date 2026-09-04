package container

import (
	"context"
	"fmt"
	"os"
	"sync"

	"xkp-agent/internal/protocol"
)

type fakeContainer struct {
	ComponentType string
	Fingerprint   string
	State         State
}

type FakeExecutor struct {
	mu         sync.Mutex
	validator  Validator
	containers map[string]fakeContainer
}

func NewFakeExecutor(validator Validator) *FakeExecutor {
	return &FakeExecutor{validator: validator, containers: map[string]fakeContainer{}}
}

func (f *FakeExecutor) InspectPair(_ context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.validator.ValidateControl(payload); err != nil {
		return PairResult{}, err
	}
	return f.inspectLocked(payload)
}

func (f *FakeExecutor) CreatePair(_ context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	workspace, err := f.validator.ValidateCreate(payload)
	if err != nil {
		return PairResult{}, err
	}
	if err := f.preflightLocked(payload); err != nil {
		return PairResult{}, err
	}
	if err := os.MkdirAll(workspace, 0750); err != nil {
		return PairResult{}, fmt.Errorf("create shared workspace: %w", err)
	}
	for _, component := range payload.Components {
		if _, exists := f.containers[component.ContainerName]; !exists {
			f.containers[component.ContainerName] = fakeContainer{
				ComponentType: component.ComponentType,
				Fingerprint:   component.ConfigFingerprint,
				State:         StateStopped,
			}
		}
	}
	return f.inspectLocked(payload)
}

func (f *FakeExecutor) StartPair(_ context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.validator.ValidateControl(payload); err != nil {
		return PairResult{}, err
	}
	if err := f.requirePairLocked(payload); err != nil {
		return PairResult{}, err
	}
	for _, component := range payload.Components {
		entry := f.containers[component.ContainerName]
		entry.State = StateRunning
		f.containers[component.ContainerName] = entry
	}
	return f.inspectLocked(payload)
}

func (f *FakeExecutor) StopPair(_ context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.validator.ValidateControl(payload); err != nil {
		return PairResult{}, err
	}
	if err := f.requirePairLocked(payload); err != nil {
		return PairResult{}, err
	}
	for _, component := range payload.Components {
		entry := f.containers[component.ContainerName]
		entry.State = StateStopped
		f.containers[component.ContainerName] = entry
	}
	return f.inspectLocked(payload)
}

func (f *FakeExecutor) RestorePair(_ context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	workspace, err := f.validator.ValidateCreate(payload)
	if err != nil {
		return PairResult{}, err
	}
	if err := f.requirePairLocked(payload); err != nil {
		return PairResult{}, err
	}
	if err := os.MkdirAll(workspace, 0750); err != nil {
		return PairResult{}, fmt.Errorf("create shared workspace: %w", err)
	}
	for _, component := range payload.Components {
		f.containers[component.ContainerName] = fakeContainer{
			ComponentType: component.ComponentType,
			Fingerprint:   component.ConfigFingerprint,
			State:         StateStopped,
		}
	}
	return f.inspectLocked(payload)
}

func (f *FakeExecutor) DeletePair(_ context.Context, payload protocol.EnvironmentPayload) (PairResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.validator.ValidateControl(payload); err != nil {
		return PairResult{}, err
	}
	for _, component := range payload.Components {
		if existing, exists := f.containers[component.ContainerName]; exists &&
			(existing.Fingerprint != component.ConfigFingerprint || existing.ComponentType != component.ComponentType) {
			return PairResult{}, fmt.Errorf("managed container identity mismatch: %s", component.ContainerName)
		}
	}
	for _, component := range payload.Components {
		delete(f.containers, component.ContainerName)
	}
	return pairWithState(payload, StateMissing), nil
}

func (f *FakeExecutor) preflightLocked(payload protocol.EnvironmentPayload) error {
	for _, component := range payload.Components {
		if existing, exists := f.containers[component.ContainerName]; exists &&
			(existing.Fingerprint != component.ConfigFingerprint || existing.ComponentType != component.ComponentType) {
			return fmt.Errorf("container identity conflict: %s", component.ContainerName)
		}
	}
	return nil
}

func (f *FakeExecutor) requirePairLocked(payload protocol.EnvironmentPayload) error {
	for _, component := range payload.Components {
		existing, exists := f.containers[component.ContainerName]
		if !exists || existing.Fingerprint != component.ConfigFingerprint || existing.ComponentType != component.ComponentType {
			return fmt.Errorf("managed container identity mismatch: %s", component.ContainerName)
		}
	}
	return nil
}

func (f *FakeExecutor) inspectLocked(payload protocol.EnvironmentPayload) (PairResult, error) {
	result := PairResult{}
	for _, component := range payload.Components {
		state := StateMissing
		if existing, exists := f.containers[component.ContainerName]; exists {
			if existing.Fingerprint != component.ConfigFingerprint || existing.ComponentType != component.ComponentType {
				return PairResult{}, fmt.Errorf("managed container identity mismatch: %s", component.ContainerName)
			}
			state = existing.State
		}
		value := ComponentResult{ComponentType: component.ComponentType, ContainerName: component.ContainerName,
			ConfigFingerprint: component.ConfigFingerprint, State: state}
		if component.ComponentType == "ANNOTATION" {
			result.Annotation = value
		} else {
			result.Editor = value
		}
	}
	return result, nil
}
