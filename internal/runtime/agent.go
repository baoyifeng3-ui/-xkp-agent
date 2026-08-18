package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"xkp-agent/internal/collect"
	"xkp-agent/internal/protocol"
)

type HeartbeatRequest struct {
	AgentID      string           `json:"agentId"`
	BootID       string           `json:"bootId"`
	Sequence     int64            `json:"sequence"`
	Timestamp    time.Time        `json:"timestamp"`
	AgentVersion string           `json:"agentVersion"`
	Metrics      collect.Snapshot `json:"metrics"`
}
type HeartbeatAck struct {
	Accepted   bool  `json:"accepted"`
	Sequence   int64 `json:"sequence"`
	ServerTime int64 `json:"serverTime"`
}

type Gatherer interface {
	Snapshot(context.Context) collect.Snapshot
}
type Transport interface {
	Heartbeat(context.Context, HeartbeatRequest) (HeartbeatAck, error)
	PollCommands(context.Context, int) ([]json.RawMessage, error)
}

type CommandHandler interface {
	Dispatch(context.Context, protocol.Command) error
	RetryPending(context.Context) (bool, error)
}

type Agent struct {
	agentID           string
	version           string
	bootID            string
	gatherer          Gatherer
	transport         Transport
	commandHandler    CommandHandler
	mu                sync.Mutex
	nextSequence      int64
	backoff           time.Duration
	heartbeatInterval time.Duration
}

func NewAgent(agentID, version string, gatherer Gatherer, transport Transport,
	commandHandler CommandHandler) *Agent {
	if gatherer == nil || transport == nil || commandHandler == nil {
		panic("agent runtime dependencies are required")
	}
	return &Agent{agentID: agentID, version: version, bootID: newBootID(), gatherer: gatherer, transport: transport,
		commandHandler: commandHandler, nextSequence: 1, backoff: time.Second, heartbeatInterval: 5 * time.Second}
}

func (a *Agent) BootID() string                { return a.bootID }
func (a *Agent) NextSequence() int64           { a.mu.Lock(); defer a.mu.Unlock(); return a.nextSequence }
func (a *Agent) CurrentBackoff() time.Duration { a.mu.Lock(); defer a.mu.Unlock(); return a.backoff }

func (a *Agent) SendOnce(ctx context.Context) error {
	a.mu.Lock()
	sequence := a.nextSequence
	a.mu.Unlock()
	request := HeartbeatRequest{AgentID: a.agentID, BootID: a.bootID, Sequence: sequence, Timestamp: time.Now().UTC(), AgentVersion: a.version, Metrics: a.gatherer.Snapshot(ctx)}
	ack, err := a.transport.Heartbeat(ctx, request)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.backoff *= 2
		if a.backoff > 30*time.Second {
			a.backoff = 30 * time.Second
		}
		return err
	}
	if ack.Sequence != sequence {
		return fmt.Errorf("heartbeat acknowledgement sequence mismatch")
	}
	// accepted=false means the management server already committed this sequence.
	a.nextSequence = sequence + 1
	a.backoff = time.Second
	return nil
}

func (a *Agent) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.heartbeatLoop(ctx) }()
	go func() { defer wg.Done(); a.commandLoop(ctx) }()
	<-ctx.Done()
	wg.Wait()
	return nil
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	for {
		err := a.SendOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		delay := a.heartbeatInterval
		if err != nil {
			delay = jitter(a.CurrentBackoff())
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (a *Agent) commandLoop(ctx context.Context) {
	for {
		err := a.processCommandsOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			timer := time.NewTimer(jitter(time.Second))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (a *Agent) processCommandsOnce(ctx context.Context) error {
	retried, err := a.commandHandler.RetryPending(ctx)
	if err != nil {
		return err
	}
	if retried {
		return nil
	}
	commands, err := a.transport.PollCommands(ctx, 25)
	if err != nil {
		return err
	}
	for _, raw := range commands {
		command, decodeErr := protocol.DecodeCommand(raw)
		if decodeErr != nil {
			return fmt.Errorf("reject invalid command envelope: %w", decodeErr)
		}
		if dispatchErr := a.commandHandler.Dispatch(ctx, command); dispatchErr != nil {
			return dispatchErr
		}
	}
	return nil
}

func jitter(delay time.Duration) time.Duration {
	var value [1]byte
	if _, err := rand.Read(value[:]); err != nil {
		return delay
	}
	factor := 0.8 + float64(value[0])/255*0.4
	return time.Duration(float64(delay) * factor)
}

func newBootID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("secure random source unavailable")
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(value[:])
	return hexValue[0:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:32]
}
