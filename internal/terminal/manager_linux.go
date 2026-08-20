//go:build linux

package terminal

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"xkp-agent/internal/protocol"
)

type linuxRunner struct {
	api         ticketClient
	development bool
	dialer      *websocket.Dialer
}

type ticketClient interface {
	ExchangeTerminalTicket(context.Context, string, string, string) (protocol.TerminalTicket, error)
	Credential() string
	TLSConfig() *tls.Config
}

func NewRunner(api ticketClient, development bool) Runner { return NewLinuxRunner(api, development) }

func NewLinuxRunner(api ticketClient, development bool) *linuxRunner {
	d := websocket.DefaultDialer
	if api != nil {
		d = &websocket.Dialer{TLSClientConfig: api.TLSConfig(), HandshakeTimeout: 10 * time.Second}
	}
	return &linuxRunner{api: api, development: development, dialer: d}
}

func (r *linuxRunner) Start(ctx context.Context, command protocol.Command) (<-chan error, error) {
	if command.Terminal == nil || command.Terminal.SessionID == "" || r.api == nil {
		return nil, &Error{Code: "TERMINAL_COMMAND_INVALID"}
	}
	if err := validateRelayURL(command.Terminal.RelayURL, command.Terminal.SessionID, r.development); err != nil {
		return nil, &Error{Code: "TERMINAL_START_FAILED"}
	}
	ticket, err := r.api.ExchangeTerminalTicket(ctx, command.Terminal.SessionID, command.CommandID, command.LeaseToken)
	if err != nil {
		return nil, &Error{Code: "TERMINAL_TICKET_FAILED"}
	}
	path, err := relayPath(command.Terminal.RelayURL, command.Terminal.SessionID)
	if err != nil {
		return nil, &Error{Code: "TERMINAL_START_FAILED"}
	}
	headers := http.Header{"Authorization": []string{"Bearer " + r.api.Credential()}}
	d := *r.dialer
	d.Subprotocols = []string{stableSubprotocol, "xkp-terminal-ticket." + ticket.Ticket}
	conn, _, err := d.DialContext(ctx, path, headers)
	if err != nil {
		return nil, &Error{Code: "TERMINAL_RELAY_FAILED"}
	}
	cmd := exec.Command("/bin/bash", "--noprofile", "--norc")
	cmd.Dir = "/root"
	cmd.Env = []string{"HOME=/root", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "TERM=xterm-256color", "SHELL=/bin/bash", "USER=root", "LOGNAME=root", "LANG=C.UTF-8"}
	ptmx, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		conn.Close()
		return nil, &Error{Code: "TERMINAL_PTY_FAILED"}
	}
	done := make(chan error, 1)
	go func() {
		done <- relaySession(ctx, conn, ptmx, cmd, command.Terminal.IdleTimeoutSeconds, command.Terminal.AbsoluteExpiresAt)
	}()
	return done, nil
}

func relaySession(ctx context.Context, conn *websocket.Conn, ptmx *os.File, cmd *exec.Cmd, idle int, absolute time.Time) error {
	defer conn.Close()
	defer ptmx.Close()
	defer terminateProcess(cmd)
	conn.SetReadLimit(maxRelayFrame)
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	conn.SetPongHandler(func(string) error { conn.SetReadDeadline(time.Now().Add(30 * time.Second)); return nil })
	lastIO := time.Now()
	idleDur := time.Duration(idle) * time.Second
	if idleDur <= 0 {
		idleDur = 600 * time.Second
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	errCh := make(chan error, 2)
	go func() {
		b := make([]byte, maxRelayFrame)
		for {
			n, e := ptmx.Read(b)
			if n > 0 {
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if e2 := conn.WriteMessage(websocket.BinaryMessage, b[:n]); e2 != nil {
					errCh <- e2
					return
				}
				lastIO = time.Now()
			}
			if e != nil {
				errCh <- e
				return
			}
		}
	}()
	go func() {
		for {
			typ, b, e := conn.ReadMessage()
			if e != nil {
				errCh <- e
				return
			}
			if typ == websocket.BinaryMessage {
				if len(b) > maxRelayFrame {
					errCh <- fmt.Errorf("frame too large")
					return
				}
				if _, e = ptmx.Write(b); e != nil {
					errCh <- e
					return
				}
				lastIO = time.Now()
			} else if typ == websocket.TextMessage {
				var c struct {
					Type string `json:"type"`
					Cols int    `json:"cols"`
					Rows int    `json:"rows"`
				}
				if json.Unmarshal(b, &c) != nil {
					errCh <- fmt.Errorf("invalid control")
					return
				}
				if c.Type == "resize" {
					if c.Cols < 20 || c.Cols > 500 || c.Rows < 5 || c.Rows > 200 {
						errCh <- fmt.Errorf("invalid resize")
						return
					}
					_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(c.Cols), Rows: uint16(c.Rows)})
				} else if c.Type != "ping" && c.Type != "pong" && c.Type != "close" {
					errCh <- fmt.Errorf("invalid control")
					return
				}
			}
		}
	}()
	for {
		select {
		case e := <-errCh:
			return e
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if time.Since(lastIO) > idleDur {
				return &Error{Code: "TERMINAL_IDLE_TIMEOUT"}
			}
			if !absolute.IsZero() && time.Now().After(absolute) {
				return &Error{Code: "TERMINAL_ABSOLUTE_TIMEOUT"}
			}
		}
	}
}

func terminateProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
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
