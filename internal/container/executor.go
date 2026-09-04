package container

import (
	"context"

	"xkp-agent/internal/protocol"
)

type State string

const (
	StateMissing State = "MISSING"
	StateStopped State = "STOPPED"
	StateRunning State = "RUNNING"
)

type ComponentResult struct {
	ComponentType     string `json:"componentType"`
	ContainerName     string `json:"containerName"`
	ConfigFingerprint string `json:"configFingerprint"`
	State             State  `json:"state"`
}

type PairResult struct {
	Annotation ComponentResult `json:"annotation"`
	Editor     ComponentResult `json:"editor"`
}

type Executor interface {
	InspectPair(context.Context, protocol.EnvironmentPayload) (PairResult, error)
	CreatePair(context.Context, protocol.EnvironmentPayload) (PairResult, error)
	StartPair(context.Context, protocol.EnvironmentPayload) (PairResult, error)
	StopPair(context.Context, protocol.EnvironmentPayload) (PairResult, error)
	RestorePair(context.Context, protocol.EnvironmentPayload) (PairResult, error)
	DeletePair(context.Context, protocol.EnvironmentPayload) (PairResult, error)
}
