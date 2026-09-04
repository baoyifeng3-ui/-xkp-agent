package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
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

type UpgradeManager interface {
	Prepare(context.Context, protocol.UpgradePayload) error
	Activate() error
}

type ImageDeploymentManager interface {
	Deploy(context.Context, protocol.ImageDeploymentPayload,
		func(int64, int64, string) error) (map[string]interface{}, error)
}
type FileTransferManager interface {
	Transfer(context.Context, protocol.FileTransferPayload) error
}
type ModelWorkspaceManager interface {
	List(context.Context, string) ([]string, error)
	Deploy(context.Context, protocol.ModelWorkspacePayload) error
}

type CommandDispatcher struct {
	transport       CommandTransport
	power           power.Controller
	store           CommandStore
	executor        container.Executor
	grant           OperationGrantStore
	terminal        terminalpkg.TerminalManager
	upgrade         UpgradeManager
	imageDeployment ImageDeploymentManager
	fileTransfer    FileTransferManager
	modelWorkspace  ModelWorkspaceManager
}

func (d *CommandDispatcher) SetFileTransferManager(manager FileTransferManager) {
	d.fileTransfer = manager
}

func (d *CommandDispatcher) SetModelWorkspaceManager(manager ModelWorkspaceManager) {
	d.modelWorkspace = manager
}

func (d *CommandDispatcher) SetUpgradeManager(manager UpgradeManager) {
	if manager == nil {
		panic("upgrade manager is required")
	}
	d.upgrade = manager
}

func (d *CommandDispatcher) SetImageDeploymentManager(manager ImageDeploymentManager) {
	if manager == nil {
		panic("image deployment manager is required")
	}
	d.imageDeployment = manager
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
	if command.Type == protocol.DeployImage {
		return d.dispatchImageDeployment(ctx, command)
	}
	if command.Type == protocol.TransferFile {
		return d.dispatchFileTransfer(ctx, command)
	}
	if command.Type == protocol.ModelWorkspace {
		return d.dispatchModelWorkspace(ctx, command)
	}
	if command.Type == protocol.ExecuteTerminalInput {
		return d.dispatchTerminalInput(ctx, command)
	}
	if command.Type == protocol.UpgradeAgent {
		return d.dispatchUpgrade(ctx, command)
	}
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

func (d *CommandDispatcher) dispatchFileTransfer(ctx context.Context, command protocol.Command) error {
	if command.FileTransfer == nil || d.fileTransfer == nil {
		return fmt.Errorf("file transfer command is invalid")
	}
	if err := d.transport.StartCommand(ctx, command.CommandID, command.LeaseToken); err != nil {
		return err
	}
	err := d.fileTransfer.Transfer(ctx, *command.FileTransfer)
	result := protocol.CommandResult{LeaseToken: command.LeaseToken, Success: err == nil, Code: "FILE_TRANSFER_SUCCEEDED", Message: "file transferred"}
	if err != nil {
		result.Code = "FILE_TRANSFER_FAILED"
		result.Message = boundedPlainMessage(err.Error())
	}
	if report := d.transport.FinishCommand(ctx, command.CommandID, result); report != nil {
		return report
	}
	return err
}

func (d *CommandDispatcher) dispatchModelWorkspace(ctx context.Context, command protocol.Command) error {
	if command.ModelWorkspace == nil || d.modelWorkspace == nil {
		return fmt.Errorf("model workspace command is invalid")
	}
	if err := d.transport.StartCommand(ctx, command.CommandID, command.LeaseToken); err != nil {
		return err
	}
	result := protocol.CommandResult{LeaseToken: command.LeaseToken, Success: true}
	var operationErr error
	if command.ModelWorkspace.Action == "LIST" {
		var files []string
		files, operationErr = d.modelWorkspace.List(ctx, command.ModelWorkspace.ContainerName)
		result.Code, result.Message = "MODEL_FILES_LISTED", "model files listed"
		result.Details = map[string]interface{}{"files": files}
	} else {
		operationErr = d.modelWorkspace.Deploy(ctx, *command.ModelWorkspace)
		result.Code, result.Message = "MODEL_DEPLOYED", "model files deployed"
	}
	if operationErr != nil {
		result.Success = false
		result.Code = "MODEL_WORKSPACE_FAILED"
		result.Message = boundedPlainMessage(operationErr.Error())
	}
	if reportErr := d.transport.FinishCommand(ctx, command.CommandID, result); reportErr != nil {
		return reportErr
	}
	return operationErr
}

func (d *CommandDispatcher) dispatchImageDeployment(ctx context.Context, command protocol.Command) error {
	if command.Version != 1 || command.ImageDeployment == nil || d.imageDeployment == nil {
		return fmt.Errorf("image deployment command is invalid")
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
				Code: "EXECUTION_OUTCOME_UNKNOWN", Message: "Agent restarted before image deployment outcome was recorded"}
			stored.Result = &unknown
			if err := d.store.Save(*stored); err != nil {
				return err
			}
		}
		return d.reportStored(ctx, *stored)
	}
	if err := d.transport.StartCommand(ctx, command.CommandID, command.LeaseToken); err != nil {
		return fmt.Errorf("acknowledge image deployment: %w", err)
	}
	state := StoredCommand{CommandID: command.CommandID, LeaseToken: command.LeaseToken}
	if err := d.store.Save(state); err != nil {
		return fmt.Errorf("persist image deployment start: %w", err)
	}
	if err := d.transport.FinishCommand(ctx, command.CommandID, protocol.CommandResult{
		LeaseToken: command.LeaseToken, Success: true, Code: "RUNNING",
		Message: "image archive is downloading and loading",
	}); err != nil {
		return fmt.Errorf("report image deployment progress: %w", err)
	}
	reportProgress := func(transferred, total int64, stage string) error {
		percent := 0
		if total > 0 {
			percent = int(transferred * 100 / total)
		}
		return d.transport.FinishCommand(ctx, command.CommandID, protocol.CommandResult{
			LeaseToken: command.LeaseToken, Success: true, Code: "RUNNING",
			Message: "image deployment is in progress", Details: map[string]interface{}{
				"transferredBytes": transferred, "totalBytes": total,
				"percent": percent, "stage": stage,
			},
		})
	}
	details, deployErr := d.imageDeployment.Deploy(ctx, *command.ImageDeployment, reportProgress)
	result := protocol.CommandResult{LeaseToken: command.LeaseToken, Success: deployErr == nil,
		Code: "SUCCEEDED", Message: "image deployment completed", Details: details}
	if deployErr == nil && details != nil {
		if already, ok := details["alreadyPresent"].(bool); ok && already {
			result.Code = "IMAGE_ALREADY_PRESENT"
			result.Message = "image is already up to date"
		}
	}
	if deployErr != nil {
		result.Code = "IMAGE_DEPLOYMENT_FAILED"
		result.Message = boundedPlainMessage(deployErr.Error())
	}
	state.Result = &result
	if err := d.store.Save(state); err != nil {
		return fmt.Errorf("persist image deployment result: %w", err)
	}
	if err := d.reportStored(ctx, state); err != nil {
		return fmt.Errorf("report image deployment: %w", err)
	}
	return deployErr
}

func (d *CommandDispatcher) dispatchTerminalInput(ctx context.Context, command protocol.Command) error {
	if command.Version != 1 || command.TerminalInput == nil {
		return fmt.Errorf("terminal input command is invalid")
	}
	if err := d.transport.StartCommand(ctx, command.CommandID, command.LeaseToken); err != nil {
		return err
	}
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "/bin/bash", "--noprofile", "--norc", "-lc", command.TerminalInput.Data)
	cmd.Dir = "/root"
	cmd.Env = []string{"HOME=/root", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	data := output.Bytes()
	if len(data) > 65536 {
		data = data[len(data)-65536:]
	}
	result := protocol.CommandResult{LeaseToken: command.LeaseToken, Success: err == nil,
		Code: "TERMINAL_INPUT_COMPLETED", Message: "command completed",
		Details: map[string]interface{}{"sessionId": command.TerminalInput.SessionID, "output": string(data)}}
	if err != nil {
		result.Code = "TERMINAL_INPUT_FAILED"
		result.Message = boundedPlainMessage(err.Error())
	}
	if reportErr := d.transport.FinishCommand(ctx, command.CommandID, result); reportErr != nil {
		return reportErr
	}
	return err
}

func (d *CommandDispatcher) dispatchUpgrade(ctx context.Context, command protocol.Command) error {
	if command.Version != 1 || command.Upgrade == nil || d.upgrade == nil {
		return fmt.Errorf("upgrade command is invalid")
	}
	if err := d.transport.StartCommand(ctx, command.CommandID, command.LeaseToken); err != nil {
		return err
	}
	if err := d.upgrade.Prepare(ctx, *command.Upgrade); err != nil {
		result := protocol.CommandResult{LeaseToken: command.LeaseToken, Success: false, Code: "AGENT_UPGRADE_FAILED", Message: boundedPlainMessage(err.Error())}
		_ = d.transport.FinishCommand(ctx, command.CommandID, result)
		return err
	}
	result := protocol.CommandResult{LeaseToken: command.LeaseToken, Success: true, Code: "AGENT_UPGRADE_STAGED", Message: "Agent upgrade verified and scheduled"}
	if err := d.transport.FinishCommand(ctx, command.CommandID, result); err != nil {
		return err
	}
	return d.upgrade.Activate()
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
	startErr := d.terminal.Start(ctx, command)
	if startErr != nil {
		var activeErr *terminalpkg.Error
		if errors.As(startErr, &activeErr) && activeErr.Code == "TERMINAL_ALREADY_ACTIVE" {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			closeErr := d.terminal.Close(closeCtx, "operator-request")
			cancel()
			if closeErr == nil {
				startErr = d.terminal.Start(ctx, command)
			}
		}
	}
	if startErr != nil {
		code := "TERMINAL_START_FAILED"
		var terminalErr *terminalpkg.Error
		if errors.As(startErr, &terminalErr) {
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
		protocol.StopTrainingEnvironment, protocol.RestoreTrainingEnvironment,
		protocol.DeleteTrainingEnvironment,
		protocol.CreateCompetitionEnvironment, protocol.StartCompetitionEnvironment,
		protocol.StopCompetitionEnvironment, protocol.RestoreCompetitionEnvironment,
		protocol.DeleteCompetitionEnvironment:
		return true
	default:
		return false
	}
}

func isEnvironmentStop(commandType protocol.CommandType) bool {
	return commandType == protocol.StopTrainingEnvironment || commandType == protocol.StopCompetitionEnvironment
}

func (d *CommandDispatcher) dispatchEnvironment(ctx context.Context, command protocol.Command) error {
	if command.Type == protocol.StartTrainingEnvironment || command.Type == protocol.StopTrainingEnvironment ||
		command.Type == protocol.StartCompetitionEnvironment || command.Type == protocol.StopCompetitionEnvironment {
		return d.dispatchEnvironmentControl(ctx, command)
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

func (d *CommandDispatcher) dispatchEnvironmentControl(ctx context.Context, command protocol.Command) error {
	if err := d.transport.StartCommand(ctx, command.CommandID, command.LeaseToken); err != nil {
		return fmt.Errorf("acknowledge command start: %w", err)
	}
	result, executionErr := d.executeEnvironment(ctx, command)
	result.LeaseToken = command.LeaseToken
	if err := d.transport.FinishCommand(ctx, command.CommandID, result); err != nil {
		return err
	}
	return executionErr
}

func (d *CommandDispatcher) executeEnvironment(ctx context.Context, command protocol.Command) (protocol.CommandResult, error) {
	if !isEnvironmentStop(command.Type) && d.grant != nil &&
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
	case protocol.CreateCompetitionEnvironment:
		pair, err = d.executor.CreatePair(ctx, *command.Environment)
	case protocol.StartTrainingEnvironment:
		pair, err = d.executor.StartPair(ctx, *command.Environment)
	case protocol.StartCompetitionEnvironment:
		pair, err = d.executor.StartPair(ctx, *command.Environment)
	case protocol.StopTrainingEnvironment:
		pair, err = d.executor.StopPair(ctx, *command.Environment)
	case protocol.StopCompetitionEnvironment:
		pair, err = d.executor.StopPair(ctx, *command.Environment)
	case protocol.RestoreTrainingEnvironment:
		pair, err = d.executor.RestorePair(ctx, *command.Environment)
	case protocol.RestoreCompetitionEnvironment:
		pair, err = d.executor.RestorePair(ctx, *command.Environment)
	case protocol.DeleteTrainingEnvironment, protocol.DeleteCompetitionEnvironment:
		pair, err = d.executor.DeletePair(ctx, *command.Environment)
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
		protocol.CreateTrainingEnvironment:     "ENVIRONMENT_CREATED",
		protocol.CreateCompetitionEnvironment:  "ENVIRONMENT_CREATED",
		protocol.StartTrainingEnvironment:      "ENVIRONMENT_STARTED",
		protocol.StartCompetitionEnvironment:   "ENVIRONMENT_STARTED",
		protocol.StopTrainingEnvironment:       "ENVIRONMENT_STOPPED",
		protocol.StopCompetitionEnvironment:    "ENVIRONMENT_STOPPED",
		protocol.RestoreTrainingEnvironment:    "ENVIRONMENT_RESTORED",
		protocol.RestoreCompetitionEnvironment: "ENVIRONMENT_RESTORED",
		protocol.DeleteTrainingEnvironment:     "ENVIRONMENT_DELETED",
		protocol.DeleteCompetitionEnvironment:  "ENVIRONMENT_DELETED",
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
