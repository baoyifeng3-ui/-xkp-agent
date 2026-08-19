package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"xkp-agent/internal/container"
	"xkp-agent/internal/power"
	"xkp-agent/internal/protocol"
)

const maxCommandResultRunes = 512

type CommandTransport interface {
	StartCommand(context.Context, string, string) error
	FinishCommand(context.Context, string, protocol.CommandResult) error
}

type CommandDispatcher struct {
	transport CommandTransport
	power     power.Controller
	store     CommandStore
	executor  container.Executor
}

func NewCommandDispatcher(transport CommandTransport, powerController power.Controller) *CommandDispatcher {
	return NewCommandDispatcherWithStore(transport, powerController, &memoryCommandStore{})
}

func NewCommandDispatcherWithStore(transport CommandTransport, powerController power.Controller,
	store CommandStore) *CommandDispatcher {
	if transport == nil || powerController == nil {
		panic("command dispatcher dependencies are required")
	}
	if store == nil {
		panic("command store is required")
	}
	return &CommandDispatcher{transport: transport, power: powerController, store: store}
}

func NewCommandDispatcherWithExecutor(transport CommandTransport, powerController power.Controller,
	store CommandStore, executor container.Executor) *CommandDispatcher {
	dispatcher := NewCommandDispatcherWithStore(transport, powerController, store)
	if executor == nil {
		panic("container executor is required")
	}
	dispatcher.executor = executor
	return dispatcher
}

func (d *CommandDispatcher) Dispatch(ctx context.Context, command protocol.Command) error {
	if command.Version != 1 || (command.Type != protocol.ShutdownServer && !isEnvironmentCommand(command.Type)) {
		return fmt.Errorf("unsupported command type or version")
	}
	if command.Type != protocol.ShutdownServer {
		if d.executor == nil || command.Environment == nil {
			return fmt.Errorf("container executor is not configured")
		}
		return d.dispatchEnvironment(ctx, command)
	}
	stored, err := d.store.Load()
	if err != nil {
		return err
	}
	if stored != nil {
		if stored.CommandID != command.CommandID || stored.LeaseToken != command.LeaseToken {
			return fmt.Errorf("another command result is pending")
		}
		if stored.Result == nil {
			unknown := protocol.CommandResult{LeaseToken: command.LeaseToken, Success: false,
				Code: "EXECUTION_OUTCOME_UNKNOWN", Message: "Agent restarted before command outcome was recorded"}
			stored.Result = &unknown
			if err := d.store.Save(*stored); err != nil {
				return err
			}
		}
		return d.reportStored(ctx, *stored)
	}
	if err := d.transport.StartCommand(ctx, command.CommandID, command.LeaseToken); err != nil {
		return fmt.Errorf("acknowledge command start: %w", err)
	}
	state := StoredCommand{CommandID: command.CommandID, LeaseToken: command.LeaseToken}
	if err := d.store.Save(state); err != nil {
		return fmt.Errorf("persist command start: %w", err)
	}

	powerErr := d.power.Shutdown(ctx)
	result := protocol.CommandResult{
		LeaseToken: command.LeaseToken,
		Success:    powerErr == nil,
		Code:       "SHUTDOWN_ACCEPTED",
		Message:    "systemd accepted poweroff",
	}
	if powerErr != nil {
		result.Success = false
		result.Code = "SHUTDOWN_FAILED"
		if errors.Is(powerErr, context.Canceled) || errors.Is(powerErr, context.DeadlineExceeded) {
			result.Code = "COMMAND_CANCELLED"
		}
		result.Message = boundedPlainMessage(powerErr.Error())
	}
	state.Result = &result
	if err := d.store.Save(state); err != nil {
		return fmt.Errorf("persist command result: %w", err)
	}
	if err := d.reportStored(ctx, state); err != nil {
		return err
	}
	if powerErr != nil {
		return fmt.Errorf("execute shutdown: %w", powerErr)
	}
	return nil
}

func isEnvironmentCommand(commandType protocol.CommandType) bool {
	switch commandType {
	case protocol.CreateTrainingEnvironment, protocol.StartTrainingEnvironment,
		protocol.StopTrainingEnvironment, protocol.RestoreTrainingEnvironment:
		return true
	default:
		return false
	}
}

func (d *CommandDispatcher) dispatchEnvironment(ctx context.Context, command protocol.Command) error {
	stored, err := d.store.Load()
	if err != nil {
		return err
	}
	if stored != nil {
		if stored.CommandID != command.CommandID || stored.LeaseToken != command.LeaseToken {
			return fmt.Errorf("another command result is pending")
		}
		if stored.Result == nil {
			unknown := protocol.CommandResult{LeaseToken: command.LeaseToken, Success: false,
				Code: "EXECUTION_OUTCOME_UNKNOWN", Message: "Agent restarted before command outcome was recorded"}
			stored.Result = &unknown
			if err := d.store.Save(*stored); err != nil {
				return err
			}
		}
		return d.reportStored(ctx, *stored)
	}
	if err := d.transport.StartCommand(ctx, command.CommandID, command.LeaseToken); err != nil {
		return fmt.Errorf("acknowledge command start: %w", err)
	}
	state := StoredCommand{CommandID: command.CommandID, LeaseToken: command.LeaseToken}
	if err := d.store.Save(state); err != nil {
		return fmt.Errorf("persist command start: %w", err)
	}

	result, executionErr := d.executeEnvironment(ctx, command)
	result.LeaseToken = command.LeaseToken
	state.Result = &result
	if err := d.store.Save(state); err != nil {
		return fmt.Errorf("persist command result: %w", err)
	}
	if err := d.reportStored(ctx, state); err != nil {
		return err
	}
	if executionErr != nil {
		return fmt.Errorf("execute environment command: %w", executionErr)
	}
	return nil
}

func (d *CommandDispatcher) executeEnvironment(ctx context.Context, command protocol.Command) (protocol.CommandResult, error) {
	var pair container.PairResult
	var err error
	switch command.Type {
	case protocol.CreateTrainingEnvironment:
		pair, err = d.executor.CreatePair(ctx, *command.Environment)
	case protocol.StartTrainingEnvironment:
		pair, err = d.executor.StartPair(ctx, *command.Environment)
	case protocol.StopTrainingEnvironment:
		pair, err = d.executor.StopPair(ctx, *command.Environment)
	case protocol.RestoreTrainingEnvironment:
		pair, err = d.executor.RestorePair(ctx, *command.Environment)
	default:
		return protocol.CommandResult{Success: false, Code: "ENVIRONMENT_COMMAND_UNSUPPORTED"}, fmt.Errorf("unsupported environment command")
	}
	if err != nil {
		code := "ENVIRONMENT_EXECUTION_FAILED"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = "COMMAND_CANCELLED"
		}
		return protocol.CommandResult{Success: false, Code: code, Message: boundedPlainMessage(err.Error())}, err
	}
	code := map[protocol.CommandType]string{
		protocol.CreateTrainingEnvironment:  "ENVIRONMENT_CREATED",
		protocol.StartTrainingEnvironment:   "ENVIRONMENT_STARTED",
		protocol.StopTrainingEnvironment:    "ENVIRONMENT_STOPPED",
		protocol.RestoreTrainingEnvironment: "ENVIRONMENT_RESTORED",
	}[command.Type]
	return protocol.CommandResult{Success: true, Code: code, Message: "environment operation completed", Details: map[string]interface{}{"pair": pair}}, nil
}

func (d *CommandDispatcher) RetryPending(ctx context.Context) (bool, error) {
	stored, err := d.store.Load()
	if err != nil {
		return false, err
	}
	if stored == nil {
		return false, nil
	}
	if stored.Result == nil {
		unknown := protocol.CommandResult{
			LeaseToken: stored.LeaseToken,
			Success:    false,
			Code:       "EXECUTION_OUTCOME_UNKNOWN",
			Message:    "Agent restarted before command outcome was recorded",
		}
		stored.Result = &unknown
		if err := d.store.Save(*stored); err != nil {
			return true, err
		}
	}
	return true, d.reportStored(ctx, *stored)
}

func (d *CommandDispatcher) reportStored(ctx context.Context, state StoredCommand) error {
	if state.Result == nil {
		return fmt.Errorf("stored command result is missing")
	}
	if err := d.transport.FinishCommand(ctx, state.CommandID, *state.Result); err != nil {
		return fmt.Errorf("report command result: %w", err)
	}
	if err := d.store.Clear(); err != nil {
		return fmt.Errorf("clear command state: %w", err)
	}
	return nil
}

type memoryCommandStore struct{ state *StoredCommand }

func (s *memoryCommandStore) Load() (*StoredCommand, error)  { return s.state, nil }
func (s *memoryCommandStore) Save(state StoredCommand) error { s.state = &state; return nil }
func (s *memoryCommandStore) Clear() error                   { s.state = nil; return nil }

func boundedPlainMessage(message string) string {
	cleaned := strings.Map(func(value rune) rune {
		if unicode.IsControl(value) {
			return ' '
		}
		return value
	}, message)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	runes := []rune(cleaned)
	if len(runes) > maxCommandResultRunes {
		runes = runes[:maxCommandResultRunes]
	}
	return string(runes)
}
