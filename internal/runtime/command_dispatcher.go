package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

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
}

func NewCommandDispatcher(transport CommandTransport, powerController power.Controller) *CommandDispatcher {
	if transport == nil || powerController == nil {
		panic("command dispatcher dependencies are required")
	}
	return &CommandDispatcher{transport: transport, power: powerController}
}

func (d *CommandDispatcher) Dispatch(ctx context.Context, command protocol.Command) error {
	if command.Type != protocol.ShutdownServer || command.Version != 1 {
		return fmt.Errorf("unsupported command type or version")
	}
	if err := d.transport.StartCommand(ctx, command.CommandID, command.LeaseToken); err != nil {
		return fmt.Errorf("acknowledge command start: %w", err)
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
	if err := d.transport.FinishCommand(ctx, command.CommandID, result); err != nil {
		return fmt.Errorf("report command result: %w", err)
	}
	if powerErr != nil {
		return fmt.Errorf("execute shutdown: %w", powerErr)
	}
	return nil
}

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
