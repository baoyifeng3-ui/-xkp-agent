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

	"xkp-agent/internal/config"
	"xkp-agent/internal/identity"
	"xkp-agent/internal/protocol"
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
		_, _ = w.Write([]byte(`{"state":"accepted"}`))
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
	err = c.StartCommand(context.Background(), "command-id", "lease-secret")
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret backend details") || strings.Contains(err.Error(), "lease-secret") {
		t.Fatal("command acknowledgement leaked sensitive content")
	}
}
