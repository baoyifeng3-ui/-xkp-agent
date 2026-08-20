package terminal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xkp-agent/internal/protocol"
)

type memoryRecoveryStore struct {
	mu       sync.Mutex
	state    *TerminalRecovery
	saveErr  error
	clearErr error
}

func (s *memoryRecoveryStore) Load() (*TerminalRecovery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return nil, nil
	}
	copy := *s.state
	return &copy, nil
}
func (s *memoryRecoveryStore) Save(value TerminalRecovery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.state = &value
	return nil
}
func (s *memoryRecoveryStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clearErr != nil {
		return s.clearErr
	}
	s.state = nil
	return nil
}

type runnerStub struct {
	started chan context.Context
	done    chan error
	err     error
}

func (r *runnerStub) Start(ctx context.Context, _ protocol.Command) (<-chan error, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.started <- ctx
	return r.done, nil
}

type reporterStub struct {
	mu      sync.Mutex
	results []protocol.CommandResult
	ids     []string
	failFor int
}

func (r *reporterStub) FinishCommand(_ context.Context, id string, result protocol.CommandResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	r.results = append(r.results, result)
	if len(r.results) <= r.failFor {
		return errors.New("network")
	}
	return nil
}

func TestManagerStartIsAsynchronousAndIndependentOfPollContext(t *testing.T) {
	store := &memoryRecoveryStore{}
	runner := &runnerStub{started: make(chan context.Context, 1), done: make(chan error, 1)}
	reporter := &reporterStub{}
	manager, err := NewManager(store, runner, reporter)
	if err != nil {
		t.Fatal(err)
	}
	pollCtx, cancel := context.WithCancel(context.Background())
	if err := manager.Start(pollCtx, terminalCommand()); err != nil {
		t.Fatal(err)
	}
	if !manager.Active() {
		t.Fatal("manager not active")
	}
	runnerCtx := <-runner.started
	cancel()
	select {
	case <-runnerCtx.Done():
		t.Fatal("poll cancellation stopped terminal")
	case <-time.After(20 * time.Millisecond):
	}
	runner.done <- nil
	waitInactive(t, manager)
	if store.state != nil {
		t.Fatal("recovery not cleared")
	}
	if len(reporter.results) != 1 || reporter.ids[0] != terminalCommand().CommandID ||
		!reporter.results[0].Success || reporter.results[0].Code != "TERMINAL_SESSION_CLOSED" ||
		reporter.results[0].LeaseToken != terminalCommand().LeaseToken {
		t.Fatalf("reports = %#v", reporter.results)
	}
}

func TestManagerRejectsSecondSessionWithTypedStableError(t *testing.T) {
	runner := &runnerStub{started: make(chan context.Context, 1), done: make(chan error, 1)}
	manager, _ := NewManager(&memoryRecoveryStore{}, runner, &reporterStub{})
	if err := manager.Start(context.Background(), terminalCommand()); err != nil {
		t.Fatal(err)
	}
	err := manager.Start(context.Background(), terminalCommandWithIDs("55555555-5555-4555-8555-555555555555", "88888888-8888-4888-8888-888888888888"))
	var terminalErr *Error
	if !errors.As(err, &terminalErr) || terminalErr.Code != "TERMINAL_ALREADY_ACTIVE" {
		t.Fatalf("error = %#v", err)
	}
	if !manager.Active() {
		t.Fatal("existing session disturbed")
	}
	runner.done <- nil
	waitInactive(t, manager)
}

func TestManagerRollsBackRecoveryWhenRunnerLaunchFails(t *testing.T) {
	store := &memoryRecoveryStore{}
	manager, _ := NewManager(store, &runnerStub{err: errors.New("launch")}, &reporterStub{})
	err := manager.Start(context.Background(), terminalCommand())
	if err == nil || manager.Active() || store.state != nil {
		t.Fatalf("error=%v active=%v recovery=%#v", err, manager.Active(), store.state)
	}
}

func TestManagerSaveFailureFailsClosedUntilRecoveryIsChecked(t *testing.T) {
	store := &memoryRecoveryStore{saveErr: errors.New("uncertain disk outcome")}
	runner := &runnerStub{started: make(chan context.Context, 1), done: make(chan error, 1)}
	manager, _ := NewManager(store, runner, &reporterStub{})
	if err := manager.Start(context.Background(), terminalCommand()); err == nil {
		t.Fatal("expected save failure")
	}
	err := manager.Start(context.Background(), terminalCommand())
	var terminalErr *Error
	if !errors.As(err, &terminalErr) || terminalErr.Code != "TERMINAL_RECOVERY_PENDING" {
		t.Fatalf("second start error = %#v", err)
	}
	if recovered, err := manager.RetryRecovery(context.Background()); recovered || err != nil {
		t.Fatalf("recovered=%v err=%v", recovered, err)
	}
	store.saveErr = nil
	if err := manager.Start(context.Background(), terminalCommand()); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	runner.done <- nil
	waitInactive(t, manager)
}

func TestManagerRetainsRecoveryWhenLaunchRollbackClearFails(t *testing.T) {
	store := &memoryRecoveryStore{clearErr: errors.New("disk")}
	reporter := &reporterStub{}
	manager, _ := NewManager(store, &runnerStub{err: errors.New("launch")}, reporter)
	if err := manager.Start(context.Background(), terminalCommand()); err == nil {
		t.Fatal("expected launch failure")
	}
	if manager.Active() || store.state == nil {
		t.Fatalf("active=%v recovery=%#v", manager.Active(), store.state)
	}
	err := manager.Start(context.Background(), terminalCommand())
	var terminalErr *Error
	if !errors.As(err, &terminalErr) || terminalErr.Code != "TERMINAL_RECOVERY_PENDING" {
		t.Fatalf("second start error = %#v", err)
	}
}

func TestManagerDoesNotRunRecoveryWhileSessionActive(t *testing.T) {
	store := &memoryRecoveryStore{}
	runner := &runnerStub{started: make(chan context.Context, 1), done: make(chan error, 1)}
	reporter := &reporterStub{}
	manager, _ := NewManager(store, runner, reporter)
	if err := manager.Start(context.Background(), terminalCommand()); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	if recovered, err := manager.RetryRecovery(context.Background()); recovered || err == nil {
		t.Fatalf("recovered=%v err=%v", recovered, err)
	}
	if len(reporter.results) != 0 || store.state == nil {
		t.Fatalf("reports=%#v recovery=%#v", reporter.results, store.state)
	}
	runner.done <- nil
	waitInactive(t, manager)
}

func TestManagerCloseIsIdempotentAndCancellationAware(t *testing.T) {
	runner := &runnerStub{started: make(chan context.Context, 1), done: make(chan error)}
	manager, _ := NewManager(&memoryRecoveryStore{}, runner, &reporterStub{})
	if err := manager.Start(context.Background(), terminalCommand()); err != nil {
		t.Fatal(err)
	}
	runnerCtx := <-runner.started
	go func() { <-runnerCtx.Done(); close(runner.done) }()
	if err := manager.Close(context.Background(), "agent-shutdown"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(context.Background(), "agent-shutdown"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(context.Background(), "arbitrary secret reason"); err == nil {
		t.Fatal("unsafe close reason accepted")
	}
}

func TestManagerCloseReleasesOwnershipWhenRunnerNeverCompletes(t *testing.T) {
	runner := &runnerStub{started: make(chan context.Context, 1), done: make(chan error)}
	reporter := &reporterStub{}
	manager, _ := NewManager(&memoryRecoveryStore{}, runner, reporter)
	if err := manager.Start(context.Background(), terminalCommand()); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(closeCtx, "agent-shutdown"); err != nil {
		t.Fatal(err)
	}
	if manager.Active() || len(reporter.results) != 1 || reporter.results[0].Code != "TERMINAL_SESSION_FAILED" {
		t.Fatalf("active=%v results=%#v", manager.Active(), reporter.results)
	}
}

func TestManagerRetriesCompletionReportBeforeClearingRecovery(t *testing.T) {
	store := &memoryRecoveryStore{}
	runner := &runnerStub{started: make(chan context.Context, 1), done: make(chan error, 1)}
	reporter := &reporterStub{failFor: 2}
	manager, _ := NewManager(store, runner, reporter)
	manager.retryDelay = time.Millisecond
	if err := manager.Start(context.Background(), terminalCommand()); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	runner.done <- errors.New("pty output must not appear")
	waitInactive(t, manager)
	if len(reporter.results) != 3 || reporter.results[2].Success || reporter.results[2].Code != "TERMINAL_SESSION_FAILED" ||
		reporter.results[2].Message != "terminal session failed" || store.state != nil {
		t.Fatalf("results=%#v recovery=%#v", reporter.results, store.state)
	}
}

func TestManagerStartupRecoveryReportsInterruptedAndRetainsOnFailure(t *testing.T) {
	recovery := validRecovery()
	store := &memoryRecoveryStore{state: &recovery}
	reporter := &reporterStub{failFor: 1}
	manager, err := NewManager(store, &runnerStub{}, reporter)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), terminalCommand()); err == nil {
		t.Fatal("started shell while recovery pending")
	}
	if recovered, err := manager.RetryRecovery(context.Background()); !recovered || err == nil || store.state == nil {
		t.Fatalf("recovered=%v err=%v state=%#v", recovered, err, store.state)
	}
	if recovered, err := manager.RetryRecovery(context.Background()); !recovered || err != nil || store.state != nil {
		t.Fatalf("recovered=%v err=%v state=%#v", recovered, err, store.state)
	}
	last := reporter.results[len(reporter.results)-1]
	if last.Success || last.Code != "TERMINAL_INTERRUPTED_BY_AGENT_RESTART" || last.LeaseToken != recovery.LeaseToken {
		t.Fatalf("result = %#v", last)
	}
}

func TestManagerPersistsTerminalStartFailureForRetry(t *testing.T) {
	store := &memoryRecoveryStore{}
	reporter := &reporterStub{failFor: 1}
	m, err := NewManager(store, &runnerStub{}, reporter)
	if err != nil {
		t.Fatal(err)
	}
	cmd := terminalCommand()
	if err := m.RecordStartFailure(context.Background(), cmd, "TERMINAL_START_FAILED", "terminal session could not be started"); err != nil {
		t.Fatal(err)
	}
	if store.state == nil || store.state.PendingCode != "TERMINAL_START_FAILED" {
		t.Fatalf("recovery=%#v", store.state)
	}
	if _, err := m.RetryRecovery(context.Background()); err == nil || store.state == nil {
		t.Fatalf("expected retained recovery, state=%#v err=%v", store.state, err)
	}
	reporter.failFor = 0
	if _, err := m.RetryRecovery(context.Background()); err != nil || store.state != nil {
		t.Fatalf("retry err=%v state=%#v", err, store.state)
	}
	if reporter.results[len(reporter.results)-1].Code != "TERMINAL_START_FAILED" {
		t.Fatalf("results=%#v", reporter.results)
	}
}

func TestManagerConstructorFailsClosedOnCorruptRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.json")
	if err := os.WriteFile(path, []byte(`{"ticket":"secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(NewFileRecoveryStore(path), &runnerStub{}, &reporterStub{}); err == nil {
		t.Fatal("corrupt recovery treated as empty")
	}
}

func TestManagerRejectsExpiredDirectCommand(t *testing.T) {
	manager, _ := NewManager(&memoryRecoveryStore{}, &runnerStub{}, &reporterStub{})
	manager.now = func() time.Time { return time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC) }
	err := manager.Start(context.Background(), terminalCommand())
	var terminalErr *Error
	if !errors.As(err, &terminalErr) || terminalErr.Code != "TERMINAL_COMMAND_EXPIRED" {
		t.Fatalf("error = %#v", err)
	}
}

func TestManagerRechecksExpiryAfterWaitingForRecoveryLock(t *testing.T) {
	manager, _ := NewManager(&memoryRecoveryStore{}, &runnerStub{err: errors.New("must not launch")}, &reporterStub{})
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	manager.now = func() time.Time {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return time.Now().UTC().Add(time.Hour)
		}
		return time.Now().UTC().Add(-time.Hour)
	}
	result := make(chan error, 1)
	go func() { result <- manager.Start(context.Background(), terminalCommand()) }()
	<-entered
	close(release)
	err := <-result
	var terminalErr *Error
	if !errors.As(err, &terminalErr) || terminalErr.Code != "TERMINAL_COMMAND_EXPIRED" {
		t.Fatalf("expiry error = %#v", err)
	}
}

func terminalCommand() protocol.Command {
	return terminalCommandWithIDs("77777777-7777-4777-8777-777777777777", "44444444-4444-4444-8444-444444444444")
}

func terminalCommandWithIDs(commandID, sessionID string) protocol.Command {
	now := time.Now().UTC().Truncate(time.Second)
	return protocol.Command{CommandID: commandID, Type: protocol.OpenRootTerminal, Version: 1,
		LeaseToken: "66666666-6666-4666-8666-666666666666", LeaseExpiresAt: time.Now().UTC().Add(time.Hour), Terminal: &protocol.TerminalPayload{
			SessionID: sessionID, RelayURL: "wss://management.example/terminal/v1/agent/" + sessionID,
			AgentConnectionDeadline: now.Add(time.Minute),
			IdleTimeoutSeconds:      600, AbsoluteExpiresAt: now.Add(2 * time.Hour)}}
}

func waitInactive(t *testing.T, manager *Manager) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for manager.Active() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if manager.Active() {
		t.Fatal("manager remained active")
	}
}
