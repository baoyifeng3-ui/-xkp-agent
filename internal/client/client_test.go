package client

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xkp-agent/internal/config"
	"xkp-agent/internal/identity"
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
