package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"xkp-agent/internal/container"
	"xkp-agent/internal/protocol"
	terminalpkg "xkp-agent/internal/terminal"
)

type commandTransportStub struct {
	order      []string
	startErr   error
	finishErr  error
	lastResult protocol.CommandResult
}

type terminalManagerStub struct {
	order  *[]string
	err    error
	active bool
}

func (m *terminalManagerStub) Start(context.Context, protocol.Command) error {
	*m.order = append(*m.order, "terminal-start")
	if m.err == nil {
		m.active = true
	}
	return m.err
}
func (m *terminalManagerStub) Active() bool                        { return m.active }
func (m *terminalManagerStub) Close(context.Context, string) error { m.active = false; return nil }

type panicCommandStore struct{}

func (*panicCommandStore) Load() (*StoredCommand, error) { panic("terminal used ordinary store") }
func (*panicCommandStore) Save(StoredCommand) error      { panic("terminal used ordinary store") }
func (*panicCommandStore) Clear() error                  { panic("terminal used ordinary store") }

func TestDispatcherAcknowledgesAndStartsTerminalWithoutOrdinaryStore(t *testing.T) {
	transport := &commandTransportStub{}
	manager := &terminalManagerStub{order: &transport.order}
	dispatcher := NewCommandDispatcherWithTerminal(transport, &powerStub{order: &transport.order}, &panicCommandStore{}, manager)
	started := time.Now()
	if err := dispatcher.Dispatch(context.Background(), terminalRuntimeCommand()); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("dispatcher waited for terminal lifetime")
	}
	if !reflect.DeepEqual(transport.order, []string{"start", "terminal-start"}) {
		t.Fatalf("order = %v", transport.order)
	}
}

func TestDispatcherRequiresConfiguredTerminalManagerBeforeAcknowledgement(t *testing.T) {
	transport := &commandTransportStub{}
	dispatcher := NewCommandDispatcher(transport, &powerStub{order: &transport.order})
	if err := dispatcher.Dispatch(context.Background(), terminalRuntimeCommand()); err == nil {
		t.Fatal("terminal accepted without manager")
	}
	if len(transport.order) != 0 {
		t.Fatalf("order = %v", transport.order)
	}
}

func TestDispatcherFinishesSecondActiveTerminalWithoutDisturbingFirst(t *testing.T) {
	transport := &commandTransportStub{}
	manager := &terminalManagerStub{order: &transport.order, active: true,
		err: &terminalpkg.Error{Code: "TERMINAL_ALREADY_ACTIVE"}}
	dispatcher := NewCommandDispatcherWithTerminal(transport, &powerStub{order: &transport.order}, &panicCommandStore{}, manager)
	err := dispatcher.Dispatch(context.Background(), terminalRuntimeCommand())
	if err == nil || !manager.active || !reflect.DeepEqual(transport.order, []string{"start", "terminal-start", "result"}) {
		t.Fatalf("error=%v active=%v order=%v", err, manager.active, transport.order)
	}
	if transport.lastResult.Success || transport.lastResult.Code != "TERMINAL_ALREADY_ACTIVE" ||
		transport.lastResult.LeaseToken != terminalRuntimeCommand().LeaseToken {
		t.Fatalf("result = %#v", transport.lastResult)
	}
}

func TestDispatcherReportsStableFailureWhenTerminalLaunchFailsAfterAck(t *testing.T) {
	transport := &commandTransportStub{}
	manager := &terminalManagerStub{order: &transport.order, err: &terminalpkg.Error{Code: "TERMINAL_START_FAILED"}}
	dispatcher := NewCommandDispatcherWithTerminal(transport, &powerStub{order: &transport.order}, &panicCommandStore{}, manager)
	if err := dispatcher.Dispatch(context.Background(), terminalRuntimeCommand()); err == nil {
		t.Fatal("expected failure")
	}
	if transport.lastResult.Code != "TERMINAL_START_FAILED" || transport.lastResult.Message != "terminal session could not be started" {
		t.Fatalf("result = %#v", transport.lastResult)
	}
}

func TestDispatcherReportsUnsupportedTerminalPlatform(t *testing.T) {
	transport := &commandTransportStub{}
	manager := &terminalManagerStub{order: &transport.order, err: &terminalpkg.Error{Code: "TERMINAL_UNSUPPORTED_PLATFORM"}}
	dispatcher := NewCommandDispatcherWithTerminal(transport, &powerStub{order: &transport.order}, &panicCommandStore{}, manager)
	_ = dispatcher.Dispatch(context.Background(), terminalRuntimeCommand())
	if transport.lastResult.Code != "TERMINAL_UNSUPPORTED_PLATFORM" {
		t.Fatalf("result = %#v", transport.lastResult)
	}
}

func terminalRuntimeCommand() protocol.Command {
	return protocol.Command{CommandID: "77777777-7777-4777-8777-777777777777", Type: protocol.OpenRootTerminal,
		Version: 1, LeaseToken: "66666666-6666-4666-8666-666666666666", Terminal: &protocol.TerminalPayload{
			SessionID:               "44444444-4444-4444-8444-444444444444",
			RelayURL:                "wss://management.example/terminal/v1/agent/44444444-4444-4444-8444-444444444444",
			AgentConnectionDeadline: time.Date(2026, 8, 20, 10, 1, 30, 0, time.UTC), IdleTimeoutSeconds: 600,
			AbsoluteExpiresAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)}}
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

type environmentExecutorStub struct {
	order *[]string
	err   error
	calls int
}

func (e *environmentExecutorStub) execute(operation string) (container.PairResult, error) {
	e.calls++
	*e.order = append(*e.order, operation)
	return container.PairResult{
		Annotation: container.ComponentResult{ComponentType: "ANNOTATION", ContainerName: "annotation", State: container.StateStopped},
		Editor:     container.ComponentResult{ComponentType: "EDITOR", ContainerName: "editor", State: container.StateStopped},
	}, e.err
}

func (e *environmentExecutorStub) InspectPair(context.Context, protocol.EnvironmentPayload) (container.PairResult, error) {
	return e.execute("inspect")
}
func (e *environmentExecutorStub) CreatePair(context.Context, protocol.EnvironmentPayload) (container.PairResult, error) {
	return e.execute("create")
}
func (e *environmentExecutorStub) StartPair(context.Context, protocol.EnvironmentPayload) (container.PairResult, error) {
	return e.execute("start-environment")
}
func (e *environmentExecutorStub) StopPair(context.Context, protocol.EnvironmentPayload) (container.PairResult, error) {
	return e.execute("stop-environment")
}
func (e *environmentExecutorStub) RestorePair(context.Context, protocol.EnvironmentPayload) (container.PairResult, error) {
	return e.execute("restore")
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

func TestDispatcherAcknowledgesEnvironmentCommandBeforeExecution(t *testing.T) {
	transport := &commandTransportStub{}
	power := &powerStub{order: &transport.order}
	executor := &environmentExecutorStub{order: &transport.order}
	dispatcher := NewCommandDispatcherWithExecutor(transport, power, &memoryCommandStore{}, executor)

	if err := dispatcher.Dispatch(context.Background(), environmentCommand(t, "create")); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transport.order, []string{"start", "create", "result"}) {
		t.Fatalf("order = %v", transport.order)
	}
	if !transport.lastResult.Success || transport.lastResult.Code != "ENVIRONMENT_CREATED" || transport.lastResult.Details["pair"] == nil {
		t.Fatalf("result = %#v", transport.lastResult)
	}
}

func TestDispatcherRoutesEveryEnvironmentOperation(t *testing.T) {
	tests := []struct {
		fixture string
		want    string
		code    string
	}{
		{"start", "start-environment", "ENVIRONMENT_STARTED"},
		{"stop", "stop-environment", "ENVIRONMENT_STOPPED"},
		{"restore", "restore", "ENVIRONMENT_RESTORED"},
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			transport := &commandTransportStub{}
			power := &powerStub{order: &transport.order}
			executor := &environmentExecutorStub{order: &transport.order}
			dispatcher := NewCommandDispatcherWithExecutor(transport, power, &memoryCommandStore{}, executor)
			if err := dispatcher.Dispatch(context.Background(), environmentCommand(t, test.fixture)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(transport.order, []string{"start", test.want, "result"}) || transport.lastResult.Code != test.code {
				t.Fatalf("order=%v result=%#v", transport.order, transport.lastResult)
			}
		})
	}
}

func TestDispatcherReportsEnvironmentFailure(t *testing.T) {
	transport := &commandTransportStub{}
	power := &powerStub{order: &transport.order}
	executor := &environmentExecutorStub{order: &transport.order, err: errors.New("docker\npermission denied")}
	dispatcher := NewCommandDispatcherWithExecutor(transport, power, &memoryCommandStore{}, executor)

	err := dispatcher.Dispatch(context.Background(), environmentCommand(t, "create"))
	if err == nil || transport.lastResult.Success || transport.lastResult.Code != "ENVIRONMENT_EXECUTION_FAILED" || transport.lastResult.Message != "docker permission denied" {
		t.Fatalf("error=%v result=%#v", err, transport.lastResult)
	}
}

func TestDispatcherRetriesEnvironmentResultWithoutExecutingAgain(t *testing.T) {
	transport := &commandTransportStub{finishErr: errors.New("network")}
	power := &powerStub{order: &transport.order}
	executor := &environmentExecutorStub{order: &transport.order}
	store := NewFileCommandStore(filepath.Join(t.TempDir(), "pending-environment-command.json"))
	dispatcher := NewCommandDispatcherWithExecutor(transport, power, store, executor)
	command := environmentCommand(t, "create")

	if err := dispatcher.Dispatch(context.Background(), command); err == nil {
		t.Fatal("expected result report failure")
	}
	transport.finishErr = nil
	transport.order = nil
	if err := dispatcher.Dispatch(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || !reflect.DeepEqual(transport.order, []string{"result"}) {
		t.Fatalf("calls=%d order=%v", executor.calls, transport.order)
	}
}

func TestDispatcherRejectsCreateStartAndRestoreWhenGrantExpired(t *testing.T) {
	for _, fixture := range []string{"create", "start", "restore"} {
		t.Run(fixture, func(t *testing.T) {
			transport := &commandTransportStub{}
			power := &powerStub{order: &transport.order}
			executor := &environmentExecutorStub{order: &transport.order}
			grant := NewMemoryOperationGrantStore()
			grant.Update(OperationGrant{Allowed: true, ExpiresAt: time.Now().UTC().Add(-time.Second)})
			dispatcher := NewCommandDispatcherWithGrant(transport, power, &memoryCommandStore{}, executor, grant)

			err := dispatcher.Dispatch(context.Background(), environmentCommand(t, fixture))
			if err == nil || executor.calls != 0 || transport.lastResult.Code != "ENVIRONMENT_OPERATION_NOT_GRANTED" {
				t.Fatalf("error=%v calls=%d result=%#v", err, executor.calls, transport.lastResult)
			}
		})
	}
}

func TestDispatcherAlwaysAllowsStopWhenGrantExpired(t *testing.T) {
	transport := &commandTransportStub{}
	power := &powerStub{order: &transport.order}
	executor := &environmentExecutorStub{order: &transport.order}
	grant := NewMemoryOperationGrantStore()
	grant.Update(OperationGrant{Allowed: false, ExpiresAt: time.Now().UTC().Add(-time.Minute)})
	dispatcher := NewCommandDispatcherWithGrant(transport, power, &memoryCommandStore{}, executor, grant)

	if err := dispatcher.Dispatch(context.Background(), environmentCommand(t, "stop")); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || transport.lastResult.Code != "ENVIRONMENT_STOPPED" {
		t.Fatalf("calls=%d result=%#v", executor.calls, transport.lastResult)
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

func environmentCommand(t *testing.T, operation string) protocol.Command {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "command-"+operation+"-training-environment-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	command, err := protocol.DecodeCommand(data)
	if err != nil {
		t.Fatal(err)
	}
	return command
}
