package runtime

import (
	"context"
	"errors"
	"path/filepath"
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

func TestDispatcherRetriesStoredResultWithoutExecutingPoweroffAgain(t *testing.T) {
	transport := &commandTransportStub{finishErr: errors.New("network")}
	power := &powerStub{order: &transport.order}
	statePath := filepath.Join(t.TempDir(), "pending-command.json")
	dispatcher := NewCommandDispatcherWithStore(transport, power, NewFileCommandStore(statePath))
	command := shutdownCommand()

	if err := dispatcher.Dispatch(context.Background(), command); err == nil {
		t.Fatal("expected first result report failure")
	}
	if power.calls != 1 {
		t.Fatalf("power calls = %d", power.calls)
	}
	transport.finishErr = nil
	transport.order = nil
	if err := dispatcher.Dispatch(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if power.calls != 1 {
		t.Fatalf("poweroff repeated, calls = %d", power.calls)
	}
	if !reflect.DeepEqual(transport.order, []string{"result"}) {
		t.Fatalf("retry order = %v", transport.order)
	}
}

func TestDispatcherRetriesStoredResultWithoutWaitingForCommandRedelivery(t *testing.T) {
	transport := &commandTransportStub{finishErr: errors.New("network")}
	power := &powerStub{order: &transport.order}
	statePath := filepath.Join(t.TempDir(), "pending-command.json")
	dispatcher := NewCommandDispatcherWithStore(transport, power, NewFileCommandStore(statePath))

	if err := dispatcher.Dispatch(context.Background(), shutdownCommand()); err == nil {
		t.Fatal("expected first result report failure")
	}
	transport.finishErr = nil
	transport.order = nil
	retried, err := dispatcher.RetryPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !retried || power.calls != 1 || !reflect.DeepEqual(transport.order, []string{"result"}) {
		t.Fatalf("retried=%v power calls=%d order=%v", retried, power.calls, transport.order)
	}
}

func TestDispatcherReportsUnknownOutcomeAfterRestartWithoutRepeatingPoweroff(t *testing.T) {
	transport := &commandTransportStub{}
	power := &powerStub{order: &transport.order}
	store := &memoryCommandStore{state: &StoredCommand{
		CommandID:  shutdownCommand().CommandID,
		LeaseToken: shutdownCommand().LeaseToken,
	}}
	dispatcher := NewCommandDispatcherWithStore(transport, power, store)

	retried, err := dispatcher.RetryPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !retried || power.calls != 0 {
		t.Fatalf("retried=%v power calls=%d", retried, power.calls)
	}
	if transport.lastResult.Code != "EXECUTION_OUTCOME_UNKNOWN" || transport.lastResult.Success {
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
