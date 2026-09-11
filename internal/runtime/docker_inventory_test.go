package runtime

import (
	"context"
	"errors"
	"testing"
	"xkp-agent/internal/protocol"
)

type inventoryStub struct{ calls int }

func (m *inventoryStub) Execute(context.Context, protocol.DockerInventoryPayload) (map[string]interface{}, error) {
	m.calls++
	return map[string]interface{}{"id": "image-id"}, nil
}

func TestInventoryResultRetryDoesNotRepeatDeletion(t *testing.T) {
	transport := &commandTransportStub{finishErr: errors.New("offline")}
	manager := &inventoryStub{}
	d := NewCommandDispatcher(transport, &powerStub{order: &transport.order})
	d.SetDockerInventoryManager(manager)
	command := protocol.Command{CommandID: "command", LeaseToken: "lease", Version: 1, Type: protocol.DockerInventoryAction, DockerInventory: &protocol.DockerInventoryPayload{Action: "DELETE_IMAGE", Target: "repo:latest"}}
	if err := d.Dispatch(context.Background(), command); err == nil {
		t.Fatal("expected failed report")
	}
	transport.finishErr = nil
	if err := d.Dispatch(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if manager.calls != 1 || !transport.lastResult.Success || transport.lastResult.Details["id"] != "image-id" {
		t.Fatalf("calls=%d result=%+v", manager.calls, transport.lastResult)
	}
}
