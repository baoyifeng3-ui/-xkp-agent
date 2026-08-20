//go:build linux

package terminal

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"xkp-agent/internal/protocol"
)

type ticketClient interface {
	ExchangeTerminalTicket(context.Context, string, string, string) (protocol.TerminalTicket, error)
	Credential() string
	TLSConfig() *tls.Config
}

type ptyStarter func(*exec.Cmd, *pty.Winsize, *syscall.SysProcAttr) (*os.File, error)
type linuxRunner struct {
	api         ticketClient
	development bool
	dialer      *websocket.Dialer
	startPTY    ptyStarter
	now         func() time.Time
}

func NewRunner(api ticketClient, development bool) Runner { return NewLinuxRunner(api, development) }
func NewLinuxRunner(api ticketClient, development bool) *linuxRunner {
	d := websocket.DefaultDialer
	if api != nil {
		d = &websocket.Dialer{TLSClientConfig: api.TLSConfig(), HandshakeTimeout: 10 * time.Second}
	}
	return &linuxRunner{api: api, development: development, dialer: d, startPTY: pty.StartWithAttrs, now: time.Now}
}

func rootShellCommand() *exec.Cmd {
	c := exec.Command("/bin/bash", "--noprofile", "--norc")
	c.Dir = "/root"
	c.Env = []string{"HOME=/root", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "TERM=xterm-256color", "SHELL=/bin/bash", "USER=root", "LOGNAME=root", "LANG=C.UTF-8"}
	return c
}
func rootShellAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true, Setctty: true} }

func (r *linuxRunner) Start(ctx context.Context, command protocol.Command) (<-chan error, error) {
	p := command.Terminal
	if p == nil || r.api == nil || r.startPTY == nil {
		return nil, &Error{Code: "TERMINAL_COMMAND_INVALID"}
	}
	if err := validateRelayURL(p.RelayURL, p.SessionID, r.development); err != nil {
		return nil, &Error{Code: "TERMINAL_START_FAILED"}
	}
	now := r.now()
	if !p.AgentConnectionDeadline.After(now) {
		return nil, &Error{Code: "TERMINAL_COMMAND_EXPIRED"}
	}
	startCtx, cancel := context.WithDeadline(ctx, p.AgentConnectionDeadline)
	defer cancel()
	ticket, err := r.api.ExchangeTerminalTicket(startCtx, p.SessionID, command.CommandID, command.LeaseToken)
	if err != nil {
		if startCtx.Err() != nil {
			return nil, &Error{Code: "TERMINAL_COMMAND_EXPIRED"}
		}
		return nil, &Error{Code: "TERMINAL_TICKET_FAILED"}
	}
	d := *r.dialer
	d.Subprotocols = []string{stableSubprotocol, "xkp-terminal-ticket." + ticket.Ticket}
	headers := http.Header{"Authorization": []string{"Bearer " + r.api.Credential()}}
	conn, _, err := d.DialContext(startCtx, p.RelayURL, headers)
	if err != nil {
		if startCtx.Err() != nil {
			return nil, &Error{Code: "TERMINAL_COMMAND_EXPIRED"}
		}
		return nil, &Error{Code: "TERMINAL_RELAY_FAILED"}
	}
	if conn.Subprotocol() != stableSubprotocol {
		conn.Close()
		return nil, &Error{Code: "TERMINAL_RELAY_FAILED"}
	}
	cmd := rootShellCommand()
	attrs := rootShellAttrs()
	ptmx, err := r.startPTY(cmd, nil, attrs)
	if err != nil {
		conn.Close()
		return nil, &Error{Code: "TERMINAL_PTY_FAILED"}
	}
	if err := startCtx.Err(); err != nil {
		conn.Close()
		ptmx.Close()
		terminateProcess(cmd)
		return nil, &Error{Code: "TERMINAL_COMMAND_EXPIRED"}
	}
	done := make(chan error, 1)
	go func() { done <- relaySession(ctx, conn, ptmx, cmd, p.IdleTimeoutSeconds, p.AbsoluteExpiresAt) }()
	return done, nil
}

type outbound struct {
	typ     int
	data    []byte
	control bool
}

func relaySession(ctx context.Context, conn *websocket.Conn, ptmx *os.File, cmd *exec.Cmd, idleSeconds int, absolute time.Time) error {
	relayCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer terminateProcess(cmd)
	conn.SetReadLimit(maxRelayFrame)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	now := time.Now()
	var lastIO atomic.Int64
	lastIO.Store(now.UnixNano())
	out := make(chan outbound, maxOutboundMessages)
	errs := make(chan error, 4)
	var queued atomic.Int64
	var wg sync.WaitGroup
	send := func(m outbound) bool {
		size := int64(len(m.data))
		if queued.Add(size) > maxOutboundBytes {
			queued.Add(-size)
			return false
		}
		select {
		case out <- m:
			return true
		default:
			queued.Add(-size)
			return false
		}
	}
	stop := func() { cancel(); _ = conn.Close(); _ = ptmx.Close() }
	defer func() { stop(); wg.Wait() }()
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case m := <-out:
				queued.Add(-int64(len(m.data)))
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				var e error
				if m.control {
					e = conn.WriteControl(m.typ, m.data, time.Now().Add(10*time.Second))
				} else {
					e = conn.WriteMessage(m.typ, m.data)
				}
				if e != nil {
					select {
					case errs <- e:
					default:
					}
					return
				}
			case <-ticker.C:
				if !send(outbound{typ: websocket.PingMessage, control: true}) {
					select {
					case errs <- fmt.Errorf("outbound queue full"):
					default:
					}
					return
				}
			case <-relayCtx.Done():
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		b := make([]byte, maxRelayFrame)
		bucket := newTokenBucket(time.Now())
		for {
			n, e := ptmx.Read(b)
			if n > 0 {
				at := time.Now()
				if !bucket.allow(n, at) {
					select {
					case errs <- fmt.Errorf("terminal output rate exceeded"):
					default:
					}
					return
				}
				data := append([]byte(nil), b[:n]...)
				if !send(outbound{typ: websocket.BinaryMessage, data: data}) {
					select {
					case errs <- fmt.Errorf("outbound queue full"):
					default:
					}
					return
				}
				lastIO.Store(at.UnixNano())
			}
			if e != nil {
				if relayCtx.Err() == nil {
					select {
					case errs <- e:
					default:
					}
				}
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		bucket := newTokenBucket(time.Now())
		controls := newTokenBucket(time.Now())
		for {
			typ, data, e := conn.ReadMessage()
			if e != nil {
				if relayCtx.Err() == nil {
					select {
					case errs <- e:
					default:
					}
				}
				return
			}
			at := time.Now()
			switch typ {
			case websocket.BinaryMessage:
				if len(data) > maxRelayFrame || !bucket.allow(len(data), at) {
					select {
					case errs <- fmt.Errorf("terminal input rate exceeded"):
					default:
					}
					return
				}
				if _, e = ptmx.Write(data); e != nil {
					select {
					case errs <- e:
					default:
					}
					return
				}
				lastIO.Store(at.UnixNano())
			case websocket.TextMessage:
				if !controls.allow(maxRelayFrame, at) {
					select {
					case errs <- fmt.Errorf("control rate exceeded"):
					default:
					}
					return
				}
				c, e := parseControl(data)
				if e != nil {
					select {
					case errs <- e:
					default:
					}
					return
				}
				switch c.kind {
				case "resize":
					if e = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(c.columns), Rows: uint16(c.rows)}); e != nil {
						select {
						case errs <- e:
						default:
						}
						return
					}
				case "ping":
					if !send(outbound{typ: websocket.TextMessage, data: []byte(`{"type":"pong"}`)}) {
						select {
						case errs <- fmt.Errorf("outbound queue full"):
						default:
						}
						return
					}
				case "close":
					select {
					case errs <- io.EOF:
					default:
					}
					return
				}
			}
		}
	}()
	idle := time.Duration(idleSeconds) * time.Second
	if idle <= 0 {
		idle = 600 * time.Second
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case e := <-errs:
			stop()
			if e == io.EOF {
				return nil
			}
			return &Error{Code: "TERMINAL_RELAY_FAILED"}
		case <-ctx.Done():
			stop()
			return &Error{Code: "TERMINAL_SESSION_CANCELLED"}
		case at := <-tick.C:
			if at.Sub(time.Unix(0, lastIO.Load())) >= idle {
				stop()
				return &Error{Code: "TERMINAL_IDLE_TIMEOUT"}
			}
			if !absolute.IsZero() && !at.Before(absolute) {
				stop()
				return &Error{Code: "TERMINAL_ABSOLUTE_TIMEOUT"}
			}
		}
	}
}

func terminateProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}
