package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"xkp-agent/internal/container"
	"xkp-agent/internal/power"
	"xkp-agent/internal/protocol"
	terminalpkg "xkp-agent/internal/terminal"
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
	grant     OperationGrantStore
	terminal  terminalpkg.TerminalManager
}

func NewCommandDispatcherWithTerminal(transport CommandTransport, powerController power.Controller,
	store CommandStore, manager terminalpkg.TerminalManager) *CommandDispatcher {
	dispatcher := NewCommandDispatcherWithStore(transport, powerController, store)
	if manager == nil {
		panic("terminal manager is required")
	}
	dispatcher.terminal = manager
	return dispatcher
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

func NewCommandDispatcherWithGrant(transport CommandTransport, powerController power.Controller,
	store CommandStore, executor container.Executor, grant OperationGrantStore) *CommandDispatcher {
	dispatcher := NewCommandDispatcherWithExecutor(transport, powerController, store, executor)
	if grant == nil {
		panic("operation grant store is required")
	}
	dispatcher.grant = grant
	return dispatcher
}

func NewCommandDispatcherWithGrantAndTerminal(transport CommandTransport, powerController power.Controller,
	store CommandStore, executor container.Executor, grant OperationGrantStore, manager terminalpkg.TerminalManager) *CommandDispatcher {
	d := NewCommandDispatcherWithGrant(transport, powerController, store, executor, grant)
	if manager == nil {
		panic("terminal manager is required")
	}
	d.terminal = manager
	return d
}

func (d *CommandDispatcher) Dispatch(ctx context.Context, command protocol.Command) error {
	if command.Type == protocol.OpenRootTerminal {
		return d.dispatchTerminal(ctx, command)
	}
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

func (d *CommandDispatcher) dispatchTerminal(ctx context.Context, command protocol.Command) error {
	if command.Version != 1 || command.Terminal == nil {
		return fmt.Errorf("terminal command is invalid")
	}
	if d.terminal == nil {
		return fmt.Errorf("terminal manager is not configured")
	}
	if err := d.transport.StartCommand(ctx, command.CommandID, command.LeaseToken); err != nil {
		return fmt.Errorf("acknowledge terminal command start: %w", err)
	}
	if err := d.terminal.Start(ctx, command); err != nil {
		code := "TERMINAL_START_FAILED"
		var terminalErr *terminalpkg.Error
		if errors.As(err, &terminalErr) {
			switch terminalErr.Code {
			case "TERMINAL_ALREADY_ACTIVE", "TERMINAL_RECOVERY_PENDING", "TERMINAL_COMMAND_INVALID", "TERMINAL_COMMAND_EXPIRED", "TERMINAL_START_FAILED", "TERMINAL_UNSUPPORTED_PLATFORM":
				code = terminalErr.Code
			}
		}
		message := "terminal session could not be started"
		if code == "TERMINAL_ALREADY_ACTIVE" {
			message = "another terminal session is already active"
		} else if code == "TERMINAL_RECOVERY_PENDING" {
			message = "terminal recovery report is pending"
		} else if code == "TERMINAL_COMMAND_INVALID" {
			message = "terminal command is invalid"
		} else if code == "TERMINAL_COMMAND_EXPIRED" {
			message = "terminal command has expired"
		} else if code == "TERMINAL_UNSUPPORTED_PLATFORM" {
			message = "terminal sessions are unsupported on this platform"
		}
		result := protocol.CommandResult{LeaseToken: command.LeaseToken, Success: false, Code: code, Message: message}
		if recorder, ok := d.terminal.(terminalpkg.StartFailureRecorder); ok && code == "TERMINAL_START_FAILED" {
			if persistErr := recorder.RecordStartFailure(ctx, command, code, message); persistErr != nil {
				return fmt.Errorf("persist terminal start failure: %w", persistErr)
			}
		}
		if reportErr := d.transport.FinishCommand(ctx, command.CommandID, result); reportErr != nil {
			return fmt.Errorf("report terminal start failure: %w", reportErr)
		}
		if recorder, ok := d.terminal.(terminalpkg.StartFailureRecorder); ok && code == "TERMINAL_START_FAILED" {
			if clearErr := recorder.ClearStartFailure(); clearErr != nil {
				return fmt.Errorf("clear terminal start failure: %w", clearErr)
			}
		}
		return fmt.Errorf("start terminal session: %s", code)
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
	if command.Type != protocol.StopTrainingEnvironment && d.grant != nil &&
		!d.grant.Current().Valid(time.Now().UTC()) {
		err := fmt.Errorf("environment operation grant is missing, denied, or expired")
		return protocol.CommandResult{Success: false, Code: "ENVIRONMENT_OPERATION_NOT_GRANTED",
			Message: err.Error()}, err
	}
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
