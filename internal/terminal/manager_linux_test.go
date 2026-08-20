//go:build linux

package terminal

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"xkp-agent/internal/protocol"
)

func TestLinuxRunnerRejectsInvalidCommandWithoutStartingProcess(t *testing.T) {
	r := &linuxRunner{}
	if _, err := r.Start(context.Background(), protocol.Command{}); err == nil {
		t.Fatal("expected invalid command error")
	}
}

type fakeTicketClient struct{}

func (fakeTicketClient) ExchangeTerminalTicket(context.Context, string, string, string) (protocol.TerminalTicket, error) {
	return protocol.TerminalTicket{Ticket: strings.Repeat("a", 43), ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (fakeTicketClient) Credential() string     { return "credential-secret" }
func (fakeTicketClient) TLSConfig() *tls.Config { return &tls.Config{MinVersion: tls.VersionTLS12} }

type blockingTicketClient struct{ fakeTicketClient }

func (blockingTicketClient) ExchangeTerminalTicket(ctx context.Context, _, _, _ string) (protocol.TerminalTicket, error) {
	<-ctx.Done()
	return protocol.TerminalTicket{}, ctx.Err()
}

func TestLinuxRunnerCapsStartupAtAgentConnectionDeadline(t *testing.T) {
	r := NewLinuxRunner(blockingTicketClient{}, true)
	cmd := terminalCommand()
	cmd.Terminal.RelayURL = "ws://127.0.0.1/terminal/v1/agent/" + cmd.Terminal.SessionID
	cmd.Terminal.AgentConnectionDeadline = time.Now().Add(20 * time.Millisecond)
	_, err := r.Start(context.Background(), cmd)
	var terminalErr *Error
	if !errors.As(err, &terminalErr) || terminalErr.Code != "TERMINAL_COMMAND_EXPIRED" {
		t.Fatalf("error=%v", err)
	}
}

func TestLinuxRunnerDialsExactURLWithTicketOnlyInSubprotocol(t *testing.T) {
	seen := make(chan *http.Request, 1)
	upgrader := websocket.Upgrader{Subprotocols: []string{stableSubprotocol}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		c, e := upgrader.Upgrade(w, r, nil)
		if e == nil {
			defer c.Close()
			_, _, _ = c.ReadMessage()
		}
	}))
	defer server.Close()
	session := "44444444-4444-4444-8444-444444444444"
	raw := "ws" + strings.TrimPrefix(server.URL, "http") + "/terminal/v1/agent/" + session
	r := NewLinuxRunner(fakeTicketClient{}, true)
	var writer *os.File
	r.startPTY = func(c *exec.Cmd, _ *pty.Winsize, a *syscall.SysProcAttr) (*os.File, error) {
		if c.Args[0] != "/bin/bash" || !a.Setsid || !a.Setctty || a.Setpgid {
			t.Fatal("unsafe PTY invocation")
		}
		reader, w, e := os.Pipe()
		writer = w
		return reader, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := terminalCommand()
	cmd.Terminal.SessionID = session
	cmd.Terminal.RelayURL = raw
	done, err := r.Start(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	req := <-seen
	if req.URL.RequestURI() != "/terminal/v1/agent/"+session || strings.Contains(req.URL.RequestURI(), strings.Repeat("a", 43)) {
		t.Fatalf("dial URL=%s", req.URL)
	}
	if got := req.Header.Values("Sec-WebSocket-Protocol"); len(got) == 0 || !strings.Contains(strings.Join(got, ","), "xkp-terminal-ticket."+strings.Repeat("a", 43)) {
		t.Fatalf("protocol=%v", got)
	}
	cancel()
	_ = writer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner leaked")
	}
}

func TestRootShellCommandAndAttributesAreFixed(t *testing.T) {
	c := rootShellCommand()
	if c.Path != "/bin/bash" || !reflect.DeepEqual(c.Args, []string{"/bin/bash", "--noprofile", "--norc"}) || c.Dir != "/root" {
		t.Fatalf("command=%#v dir=%q", c.Args, c.Dir)
	}
	want := []string{"HOME=/root", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "TERM=xterm-256color", "SHELL=/bin/bash", "USER=root", "LOGNAME=root", "LANG=C.UTF-8"}
	if !reflect.DeepEqual(c.Env, want) {
		t.Fatalf("env=%#v", c.Env)
	}
	a := rootShellAttrs()
	if !a.Setsid || !a.Setctty || a.Setpgid {
		t.Fatalf("attrs=%#v", a)
	}
}
