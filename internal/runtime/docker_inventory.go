package runtime

import (
	"context"
	"fmt"
	"xkp-agent/internal/protocol"
)

func (d *CommandDispatcher) dispatchDockerInventory(ctx context.Context, command protocol.Command) error {
	if command.Version != 1 || command.DockerInventory == nil || d.dockerInventory == nil {
		return fmt.Errorf("Docker inventory command is invalid")
	}
	if err := command.DockerInventory.Validate(); err != nil {
		return err
	}
	stored, err := d.store.Load()
	if err != nil {
		return err
	}
	if stored != nil {
		if stored.CommandID != command.CommandID || stored.LeaseToken != command.LeaseToken {
			return fmt.Errorf("another command result is pending")
		}
		_, err := d.RetryPending(ctx)
		return err
	}
	if err := d.transport.StartCommand(ctx, command.CommandID, command.LeaseToken); err != nil {
		return err
	}
	state := StoredCommand{CommandID: command.CommandID, LeaseToken: command.LeaseToken}
	if err := d.store.Save(state); err != nil {
		return err
	}
	details, operationErr := d.dockerInventory.Execute(ctx, *command.DockerInventory)
	result := protocol.CommandResult{LeaseToken: command.LeaseToken, Success: operationErr == nil, Code: "SUCCEEDED", Message: "Docker operation completed", Details: details}
	if operationErr != nil {
		result.Code, result.Message = "DOCKER_INVENTORY_FAILED", boundedPlainMessage(operationErr.Error())
	}
	state.Result = &result
	if err := d.store.Save(state); err != nil {
		return err
	}
	if err := d.reportStored(ctx, state); err != nil {
		return err
	}
	return operationErr
}
