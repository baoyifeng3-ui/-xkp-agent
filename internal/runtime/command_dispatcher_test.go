package runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"xkp-agent/internal/protocol"
)

type commandTransportStub struct {
	order      []string
	startErr   error
	finishErr  error
	lastResult protocol.CommandResult
}

func (t *commandTransportStub) StartCommand(context.Context, string, string) error {
	t.order = append(t.order, "start")
	return t.startErr
}

func (t *commandTransportStub) FinishCommand(_ context.Context, _ string, result protocol.CommandResult) error {
	t.order = append(t.order, "result")
	t.lastResult = result
	return t.finishErr
}

type powerStub struct {
	order *[]string
	err   error
	calls int
}

func (p *powerStub) Shutdown(context.Context) error {
	p.calls++
	*p.order = append(*p.order, "poweroff")
	return p.err
}

func TestDispatcherAcknowledgesStartBeforePoweroff(t *testing.T) {
	transport := &commandTransportStub{}
	power := &powerStub{order: &transport.order}
	dispatcher := NewCommandDispatcher(transport, power)

	if err := dispatcher.Dispatch(context.Background(), shutdownCommand()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start", "poweroff", "result"}
	if !reflect.DeepEqual(want, transport.order) {
		t.Fatalf("order = %v, want %v", transport.order, want)
	}
	if !transport.lastResult.Success || transport.lastResult.Code != "SHUTDOWN_ACCEPTED" {
		t.Fatalf("result = %#v", transport.lastResult)
	}
}

func TestDispatcherDoesNotExecuteWhenStartFails(t *testing.T) {
	transport := &commandTransportStub{startErr: errors.New("network")}
	power := &powerStub{order: &transport.order}
	err := NewCommandDispatcher(transport, power).Dispatch(context.Background(), shutdownCommand())
	if err == nil || power.calls != 0 || !reflect.DeepEqual(transport.order, []string{"start"}) {
		t.Fatalf("error=%v calls=%d order=%v", err, power.calls, transport.order)
	}
}

func TestDispatcherRejectsUnknownCommandWithoutAcknowledging(t *testing.T) {
	transport := &commandTransportStub{}
	power := &powerStub{order: &transport.order}
	command := shutdownCommand()
	command.Type = "RUN_SHELL"

	err := NewCommandDispatcher(transport, power).Dispatch(context.Background(), command)

	if err == nil || power.calls != 0 || len(transport.order) != 0 {
		t.Fatalf("error=%v calls=%d order=%v", err, power.calls, transport.order)
	}
}

func TestDispatcherReportsPowerFailureWithBoundedPlainMessage(t *testing.T) {
	transport := &commandTransportStub{}
	power := &powerStub{order: &transport.order, err: errors.New("permission\r\ndenied")}
	err := NewCommandDispatcher(transport, power).Dispatch(context.Background(), shutdownCommand())
	if err == nil {
		t.Fatal("expected shutdown failure")
	}
	if transport.lastResult.Success || transport.lastResult.Code != "SHUTDOWN_FAILED" || transport.lastResult.Message != "permission denied" {
		t.Fatalf("result = %#v", transport.lastResult)
	}
}

func TestDispatcherReportsCancellation(t *testing.T) {
	transport := &commandTransportStub{}
	power := &powerStub{order: &transport.order, err: context.Canceled}
	_ = NewCommandDispatcher(transport, power).Dispatch(context.Background(), shutdownCommand())
	if transport.lastResult.Code != "COMMAND_CANCELLED" {
		t.Fatalf("result = %#v", transport.lastResult)
	}
}

func shutdownCommand() protocol.Command {
	return protocol.Command{
		CommandID:  "11111111-2222-4333-8444-555555555555",
		Type:       protocol.ShutdownServer,
		Version:    1,
		LeaseToken: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
	}
}
