package terminal

import (
	"context"
	"fmt"
	"sync"
	"time"

	"xkp-agent/internal/protocol"
)

type TerminalManager interface {
	Start(context.Context, protocol.Command) error
	Active() bool
	Close(context.Context, string) error
}

type Runner interface {
	Start(context.Context, protocol.Command) (<-chan error, error)
}

type Reporter interface {
	FinishCommand(context.Context, string, protocol.CommandResult) error
}

type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }

type Manager struct {
	mu              sync.Mutex
	recoveryMu      sync.Mutex
	store           RecoveryStore
	runner          Runner
	reporter        Reporter
	retryDelay      time.Duration
	active          bool
	recoveryPending bool
	cancel          context.CancelFunc
	done            chan struct{}
}

func NewManager(store RecoveryStore, runner Runner, reporter Reporter) (*Manager, error) {
	if store == nil || runner == nil || reporter == nil {
		return nil, fmt.Errorf("terminal manager dependencies are required")
	}
	recovery, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("load terminal recovery: %w", err)
	}
	return &Manager{
		store: store, runner: runner, reporter: reporter, retryDelay: 100 * time.Millisecond,
		recoveryPending: recovery != nil,
	}, nil
}

func (m *Manager) Start(_ context.Context, command protocol.Command) error {
	if command.Type != protocol.OpenRootTerminal || command.Version != 1 || command.Terminal == nil {
		return &Error{Code: "TERMINAL_COMMAND_INVALID"}
	}
	recovery := TerminalRecovery{
		SessionID: command.Terminal.SessionID, CommandID: command.CommandID, LeaseToken: command.LeaseToken,
		AgentConnectionDeadline: command.Terminal.AgentConnectionDeadline,
		AbsoluteExpiresAt:       command.Terminal.AbsoluteExpiresAt,
	}
	if err := validateRecovery(recovery); err != nil {
		return &Error{Code: "TERMINAL_COMMAND_INVALID"}
	}
	m.recoveryMu.Lock()
	defer m.recoveryMu.Unlock()

	m.mu.Lock()
	if m.active {
		m.mu.Unlock()
		return &Error{Code: "TERMINAL_ALREADY_ACTIVE"}
	}
	if m.recoveryPending {
		m.mu.Unlock()
		return &Error{Code: "TERMINAL_RECOVERY_PENDING"}
	}
	sessionCtx, cancel := context.WithDeadline(context.Background(), recovery.AbsoluteExpiresAt)
	done := make(chan struct{})
	m.active, m.cancel, m.done = true, cancel, done
	m.mu.Unlock()

	if err := m.store.Save(recovery); err != nil {
		m.mu.Lock()
		m.recoveryPending = true
		m.mu.Unlock()
		m.rollbackStart(cancel, done)
		return fmt.Errorf("persist terminal recovery: %w", err)
	}
	completion, err := m.runner.Start(sessionCtx, command)
	if err != nil || completion == nil {
		clearErr := m.store.Clear()
		if clearErr != nil {
			m.mu.Lock()
			m.recoveryPending = true
			m.mu.Unlock()
		}
		m.rollbackStart(cancel, done)
		if clearErr != nil {
			return fmt.Errorf("launch terminal session and clear recovery failed")
		}
		return &Error{Code: "TERMINAL_START_FAILED"}
	}
	go m.awaitCompletion(sessionCtx, done, recovery, completion)
	return nil
}

func (m *Manager) rollbackStart(cancel context.CancelFunc, done chan struct{}) {
	cancel()
	m.mu.Lock()
	if m.done == done {
		m.active, m.cancel, m.done = false, nil, nil
		close(done)
	}
	m.mu.Unlock()
}

func (m *Manager) awaitCompletion(sessionCtx context.Context, done chan struct{}, recovery TerminalRecovery, completion <-chan error) {
	var runErr error
	select {
	case value, ok := <-completion:
		if ok {
			runErr = value
		}
	case <-sessionCtx.Done():
		runErr = sessionCtx.Err()
	}
	result := protocol.CommandResult{
		LeaseToken: recovery.LeaseToken, Success: runErr == nil,
		Code: "TERMINAL_SESSION_COMPLETED", Message: "terminal session completed",
	}
	if runErr != nil {
		result.Success = false
		result.Code = "TERMINAL_SESSION_FAILED"
		result.Message = "terminal session failed"
	}
	reported := false
	for attempt := 0; attempt < 3; attempt++ {
		reportCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := m.reporter.FinishCommand(reportCtx, recovery.CommandID, result)
		cancel()
		if err == nil {
			reported = true
			break
		}
		if attempt < 2 {
			time.Sleep(m.retryDelay)
		}
	}
	if reported {
		if err := m.store.Clear(); err == nil {
			m.mu.Lock()
			m.recoveryPending = false
			m.mu.Unlock()
		} else {
			m.mu.Lock()
			m.recoveryPending = true
			m.mu.Unlock()
		}
	} else {
		m.mu.Lock()
		m.recoveryPending = true
		m.mu.Unlock()
	}
	m.mu.Lock()
	if m.done == done {
		m.active, m.cancel, m.done = false, nil, nil
		close(done)
	}
	m.mu.Unlock()
}

func (m *Manager) Active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

func (m *Manager) Close(ctx context.Context, reason string) error {
	if !allowedCloseReason(reason) {
		return &Error{Code: "TERMINAL_CLOSE_REASON_INVALID"}
	}
	m.mu.Lock()
	if !m.active {
		m.mu.Unlock()
		return nil
	}
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func allowedCloseReason(reason string) bool {
	switch reason {
	case "agent-shutdown", "session-expired", "browser-disconnected", "operator-request":
		return true
	default:
		return false
	}
}

func (m *Manager) RetryRecovery(ctx context.Context) (bool, error) {
	m.recoveryMu.Lock()
	defer m.recoveryMu.Unlock()
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	if active {
		return false, &Error{Code: "TERMINAL_ALREADY_ACTIVE"}
	}
	recovery, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load terminal recovery: %w", err)
	}
	if recovery == nil {
		m.mu.Lock()
		m.recoveryPending = false
		m.mu.Unlock()
		return false, nil
	}
	result := protocol.CommandResult{
		LeaseToken: recovery.LeaseToken, Success: false,
		Code: "TERMINAL_INTERRUPTED_BY_AGENT_RESTART", Message: "terminal session interrupted by agent restart",
	}
	if err := m.reporter.FinishCommand(ctx, recovery.CommandID, result); err != nil {
		return true, fmt.Errorf("report terminal recovery: %w", err)
	}
	if err := m.store.Clear(); err != nil {
		return true, fmt.Errorf("clear terminal recovery: %w", err)
	}
	m.mu.Lock()
	m.recoveryPending = false
	m.mu.Unlock()
	return true, nil
}
