package client

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xkp-agent/internal/config"
	"xkp-agent/internal/identity"
	"xkp-agent/internal/protocol"
	"xkp-agent/internal/runtime"
)

func TestEnrollmentUsesConfiguredCAAndRequiresCompleteResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agentId":"agent-only"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	cert := server.Certificate()
	_ = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	c, err := New(config.Config{ManagementURL: server.URL, CACertificate: caPath, WorkspacePath: dir})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Enroll(context.Background(), "registration-secret", identity.Info{MachineDigest: strings.Repeat("a", 64), Hostname: "host", PrimaryIP: "192.168.1.2", MACAddress: "00:11:22:33:44:55"})
	if err == nil {
		t.Fatal("incomplete enrollment response accepted")
	}
	if strings.Contains(err.Error(), "registration-secret") {
		t.Fatal("registration token leaked in error")
	}
}

func TestExchangeTerminalTicketUsesAuthenticatedExactRequest(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	const sessionID = "44444444-4444-4444-8444-444444444444"
	const commandID = "77777777-7777-4777-8777-777777777777"
	const leaseToken = "66666666-6666-4666-8666-666666666666"
	const ticket = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.RequestURI() != "/agent/v1/terminal-sessions/"+sessionID+"/agent-ticket" {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		if r.Header.Get("Authorization") != "Bearer agent-secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %#v", r.Header)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 2 || body["commandId"] != commandID || body["leaseToken"] != leaseToken {
			t.Errorf("body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"ticket":"` + ticket + `","expiresAt":"2026-08-20T10:00:30Z"}`))
	}))
	defer server.Close()
	c := testClientWithClock(t, server.URL, func() time.Time { return now })
	got, err := c.ExchangeTerminalTicket(context.Background(), sessionID, commandID, leaseToken)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticket != ticket || !got.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("ticket = %#v", got)
	}
}

func TestExchangeTerminalTicketRejectsInvalidIdentifiersBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	c := testClientWithClock(t, server.URL, time.Now)
	valid := "44444444-4444-4444-8444-444444444444"
	for _, ids := range [][3]string{{"../secret", valid, valid}, {valid, "COMMAND", valid}, {valid, valid, "LEASE"}} {
		if _, err := c.ExchangeTerminalTicket(context.Background(), ids[0], ids[1], ids[2]); err == nil {
			t.Fatalf("accepted identifiers %#v", ids)
		}
	}
	if requests != 0 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestExchangeTerminalTicketStrictResponseAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	validTicket := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
	tests := []string{
		`{"ticket":"` + validTicket + `","expiresAt":"2026-08-20T10:00:30Z","extra":true}`,
		`{"ticket":"` + validTicket + `","ticket":"` + validTicket + `","expiresAt":"2026-08-20T10:00:30Z"}`,
		`{"ticket":"short","expiresAt":"2026-08-20T10:00:30Z"}`,
		`{"ticket":"` + validTicket + `","expiresAt":"2026-08-20T10:00:30.000Z"}`,
		`{"ticket":"` + validTicket + `","expiresAt":"2026-08-20T10:00:00Z"}`,
		`{"ticket":"` + validTicket + `","expiresAt":"2026-08-20T10:01:01Z"}`,
		`{"ticket":"` + validTicket + `","expiresAt":"2026-08-20T10:00:30Z"} {}`,
		strings.Repeat(" ", 4097),
	}
	for _, body := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		c := testClientWithClock(t, server.URL, func() time.Time { return now })
		_, err := c.ExchangeTerminalTicket(context.Background(), "44444444-4444-4444-8444-444444444444", "77777777-7777-4777-8777-777777777777", "66666666-6666-4666-8666-666666666666")
		server.Close()
		if err == nil {
			t.Fatalf("accepted response %q", body)
		}
	}
}

func TestExchangeTerminalTicketErrorsDoNotLeakSecrets(t *testing.T) {
	const secretBody = "backend-ticket-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, secretBody, http.StatusConflict) }))
	defer server.Close()
	c := testClientWithClock(t, server.URL, time.Now)
	_, err := c.ExchangeTerminalTicket(context.Background(), "44444444-4444-4444-8444-444444444444", "77777777-7777-4777-8777-777777777777", "66666666-6666-4666-8666-666666666666")
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("error = %v", err)
	}
	for _, secret := range []string{secretBody, "66666666-6666-4666-8666-666666666666", "agent-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

func TestExchangeTerminalTicketRejectsRedirectResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent/v1/terminal-sessions/44444444-4444-4444-8444-444444444444/agent-ticket" {
			http.Redirect(w, r, "/redirected", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"ticket":"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ","expiresAt":"2026-08-20T10:00:30Z"}`))
	}))
	defer server.Close()
	c := testClientWithClock(t, server.URL, func() time.Time { return time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC) })
	if _, err := c.ExchangeTerminalTicket(context.Background(), "44444444-4444-4444-8444-444444444444", "77777777-7777-4777-8777-777777777777", "66666666-6666-4666-8666-666666666666"); err == nil {
		t.Fatal("accepted redirected ticket response")
	}
}

func TestClientMethodsRejectRedirectsWithoutFollowingOrForwardingCredentials(t *testing.T) {
	finalHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			finalHits++
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer server.Close()
	c := testClientWithClock(t, server.URL, time.Now)
	info := identity.Info{Hostname: "host", MachineDigest: strings.Repeat("a", 64)}
	checks := []func() error{
		func() error { _, err := c.Enroll(context.Background(), "registration-secret", info); return err },
		func() error { _, err := c.Heartbeat(context.Background(), runtime.HeartbeatRequest{}); return err },
		func() error { _, err := c.PollCommands(context.Background(), 0); return err },
		func() error {
			return c.StartCommand(context.Background(), "77777777-7777-4777-8777-777777777777", "66666666-6666-4666-8666-666666666666")
		},
		func() error {
			return c.FinishCommand(context.Background(), "77777777-7777-4777-8777-777777777777", protocol.CommandResult{LeaseToken: "66666666-6666-4666-8666-666666666666"})
		},
		func() error {
			_, err := c.ExchangeTerminalTicket(context.Background(), "44444444-4444-4444-8444-444444444444", "77777777-7777-4777-8777-777777777777", "66666666-6666-4666-8666-666666666666")
			return err
		},
	}
	for i, check := range checks {
		if err := check(); err == nil {
			t.Fatalf("method %d accepted redirect", i)
		}
	}
	if finalHits != 0 {
		t.Fatalf("redirect target hits = %d", finalHits)
	}
}

func TestCommandClientRejectsPathInjectionIdentifiersBeforeRequest(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	c := testClientWithClock(t, server.URL, time.Now)
	validLease := "66666666-6666-4666-8666-666666666666"
	for _, commandID := range []string{"../secret", "77777777-7777-4777-8777-777777777777?x=1", "UPPER"} {
		if err := c.StartCommand(context.Background(), commandID, validLease); err == nil {
			t.Fatalf("accepted command id %q", commandID)
		}
		if err := c.FinishCommand(context.Background(), commandID, protocol.CommandResult{LeaseToken: validLease}); err == nil {
			t.Fatalf("accepted finish command id %q", commandID)
		}
	}
	if err := c.StartCommand(context.Background(), "77777777-7777-4777-8777-777777777777", "lease"); err == nil {
		t.Fatal("accepted invalid lease token")
	}
	if hits != 0 {
		t.Fatalf("request hits = %d", hits)
	}
}

func TestGenericCommandClientPreservesUppercaseUUIDCompatibility(t *testing.T) {
	commandID := "abcdefab-cdef-4abc-8def-abcdefabcdef"
	leaseToken := "abcdefab-cdef-4abc-8def-abcdefabcdef"
	requests := make(chan *http.Request, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(context.Background())
		if strings.HasSuffix(r.URL.Path, "/start") {
			_, _ = w.Write([]byte(`{"commandId":"ABCDEFAB-CDEF-4ABC-8DEF-ABCDEFABCDEF","agentId":"agent-id","state":"RUNNING"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	c, err := New(config.Config{ManagementURL: server.URL, AgentID: "agent-id", Credential: "agent-secret", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	upperCommandID := strings.ToUpper(commandID)
	upperLeaseToken := strings.ToUpper(leaseToken)
	if err := c.StartCommand(context.Background(), upperCommandID, upperLeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishCommand(context.Background(), upperCommandID, protocol.CommandResult{LeaseToken: upperLeaseToken}); err != nil {
		t.Fatal(err)
	}
	start, finish := <-requests, <-requests
	if start.URL.Path != "/agent/v1/commands/"+upperCommandID+"/start" || finish.URL.Path != "/agent/v1/commands/"+upperCommandID+"/result" {
		t.Fatalf("paths = %s, %s", start.URL.Path, finish.URL.Path)
	}
}

func testClientWithClock(t *testing.T, baseURL string, now func() time.Time) *Client {
	t.Helper()
	c, err := New(config.Config{ManagementURL: baseURL, AgentID: "agent-id", Credential: "agent-secret", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	c.now = now
	return c
}

func TestCommandStartAndResultUseAuthenticatedVersionOnePaths(t *testing.T) {
	requests := make(chan *http.Request, 2)
	bodies := make(chan map[string]interface{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- r.Clone(context.Background())
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/start") {
			_, _ = w.Write([]byte(`{"commandId":"11111111-2222-4333-8444-555555555555","agentId":"agent-id","state":"RUNNING"}`))
		} else {
			_, _ = w.Write([]byte(`{"state":"FAILED"}`))
		}
	}))
	defer server.Close()
	c, err := New(config.Config{ManagementURL: server.URL, AgentID: "agent-id",
		Credential: "agent-secret", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	commandID := "11111111-2222-4333-8444-555555555555"
	leaseToken := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	if err := c.StartCommand(context.Background(), commandID, leaseToken); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishCommand(context.Background(), commandID, protocol.CommandResult{
		LeaseToken: leaseToken, Success: false, Code: "SHUTDOWN_FAILED", Message: "permission denied",
	}); err != nil {
		t.Fatal(err)
	}

	startRequest, resultRequest := <-requests, <-requests
	startBody, resultBody := <-bodies, <-bodies
	if startRequest.URL.Path != "/agent/v1/commands/"+commandID+"/start" || startRequest.Header.Get("Authorization") != "Bearer agent-secret" {
		t.Fatalf("start request = %s %#v", startRequest.URL.Path, startRequest.Header)
	}
	if len(startBody) != 1 || startBody["leaseToken"] != leaseToken {
		t.Fatalf("start body = %#v", startBody)
	}
	if resultRequest.URL.Path != "/agent/v1/commands/"+commandID+"/result" || resultRequest.Header.Get("Authorization") != "Bearer agent-secret" {
		t.Fatalf("result request = %s %#v", resultRequest.URL.Path, resultRequest.Header)
	}
	if resultBody["leaseToken"] != leaseToken || resultBody["success"] != false || resultBody["code"] != "SHUTDOWN_FAILED" || resultBody["message"] != "permission denied" {
		t.Fatalf("result body = %#v", resultBody)
	}
}

func TestCommandAcknowledgementRejectsNonSuccessWithoutLeakingBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "secret backend details", http.StatusConflict)
	}))
	defer server.Close()
	c, err := New(config.Config{ManagementURL: server.URL, AgentID: "agent-id",
		Credential: "agent-secret", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	err = c.StartCommand(context.Background(), "77777777-7777-4777-8777-777777777777", "66666666-6666-4666-8666-666666666666")
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret backend details") || strings.Contains(err.Error(), "lease-secret") {
		t.Fatal("command acknowledgement leaked sensitive content")
	}
}

func TestCommandAcknowledgementRejectsUnexpectedSuccessStateOrIdentity(t *testing.T) {
	cases := []string{
		`{"commandId":"11111111-2222-4333-8444-555555555555","agentId":"agent-id","state":"SUCCEEDED"}`,
		`{"commandId":"99999999-2222-4333-8444-555555555555","agentId":"agent-id","state":"RUNNING"}`,
		`{"commandId":"11111111-2222-4333-8444-555555555555","agentId":"other-agent","state":"RUNNING"}`,
	}
	for _, body := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		c, err := New(config.Config{ManagementURL: server.URL, AgentID: "agent-id",
			Credential: "agent-secret", Development: true})
		if err != nil {
			t.Fatal(err)
		}
		err = c.StartCommand(context.Background(), "11111111-2222-4333-8444-555555555555",
			"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
		server.Close()
		if err == nil {
			t.Fatalf("unexpected acknowledgement accepted: %s", body)
		}
	}
}
