package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"xkp-agent/internal/collect"
	"xkp-agent/internal/protocol"
)

type fakeGatherer struct{}

func (fakeGatherer) Snapshot(context.Context) collect.Snapshot { return collect.Snapshot{} }

type fakeTransport struct {
	mu        sync.Mutex
	fail      bool
	accepted  bool
	sequences []int64
	block     bool
	commands  []json.RawMessage
}

func (f *fakeTransport) Heartbeat(ctx context.Context, request HeartbeatRequest) (HeartbeatAck, error) {
	if f.block {
		<-ctx.Done()
		return HeartbeatAck{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sequences = append(f.sequences, request.Sequence)
	if f.fail {
		f.fail = false
		return HeartbeatAck{}, errors.New("network")
	}
	return HeartbeatAck{Accepted: f.accepted, Sequence: request.Sequence}, nil
}
func (f *fakeTransport) PollCommands(ctx context.Context, _ int) ([]json.RawMessage, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.commands, nil
}

type commandHandlerStub struct {
	commands []protocol.Command
	err      error
}

func (h *commandHandlerStub) Dispatch(_ context.Context, command protocol.Command) error {
	h.commands = append(h.commands, command)
	return h.err
}

func TestSequenceRetriesAndDuplicateAcknowledgementAdvances(t *testing.T) {
	transport := &fakeTransport{fail: true, accepted: false}
	a := NewAgent("agent", "0.1.0", fakeGatherer{}, transport, &commandHandlerStub{})
	if err := a.SendOnce(context.Background()); err == nil {
		t.Fatal("expected first failure")
	}
	if a.NextSequence() != 1 || a.CurrentBackoff() <= time.Second {
		t.Fatalf("retry state = %d %v", a.NextSequence(), a.CurrentBackoff())
	}
	if err := a.SendOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.NextSequence() != 2 {
		t.Fatalf("next sequence = %d", a.NextSequence())
	}
	if a.CurrentBackoff() != time.Second {
		t.Fatalf("backoff was not reset: %v", a.CurrentBackoff())
	}
}

func TestBootIDChangesForEachProcessInstance(t *testing.T) {
	a := NewAgent("agent", "0.1.0", fakeGatherer{}, &fakeTransport{}, &commandHandlerStub{})
	b := NewAgent("agent", "0.1.0", fakeGatherer{}, &fakeTransport{}, &commandHandlerStub{})
	if a.BootID() == b.BootID() || a.BootID() == "" {
		t.Fatalf("boot IDs = %q %q", a.BootID(), b.BootID())
	}
}

func TestCancellationStopsHeartbeatAndLongPollPromptly(t *testing.T) {
	a := NewAgent("agent", "0.1.0", fakeGatherer{}, &fakeTransport{block: true}, &commandHandlerStub{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("runtime did not stop")
	}
}

func TestProcessCommandsOnceStrictlyDecodesBeforeDispatch(t *testing.T) {
	valid := json.RawMessage(`{"commandId":"11111111-2222-4333-8444-555555555555","type":"SHUTDOWN_SERVER","version":1,"leaseToken":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","leaseExpiresAt":"2026-08-19T12:05:00Z","payload":{}}`)
	transport := &fakeTransport{commands: []json.RawMessage{valid}}
	handler := &commandHandlerStub{}
	agent := NewAgent("agent", "0.1.0", fakeGatherer{}, transport, handler)

	if err := agent.processCommandsOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(handler.commands) != 1 || handler.commands[0].Type != protocol.ShutdownServer {
		t.Fatalf("commands = %#v", handler.commands)
	}
}

func TestProcessCommandsOnceRejectsInvalidEnvelopeBeforeDispatch(t *testing.T) {
	invalid := json.RawMessage(`{"commandId":"11111111-2222-4333-8444-555555555555","type":"SHUTDOWN_SERVER","version":1,"leaseToken":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","leaseExpiresAt":"2026-08-19T12:05:00Z","payload":{},"shell":"poweroff"}`)
	handler := &commandHandlerStub{}
	agent := NewAgent("agent", "0.1.0", fakeGatherer{}, &fakeTransport{commands: []json.RawMessage{invalid}}, handler)

	if err := agent.processCommandsOnce(context.Background()); err == nil {
		t.Fatal("expected invalid command rejection")
	}
	if len(handler.commands) != 0 {
		t.Fatalf("invalid command dispatched: %#v", handler.commands)
	}
}
