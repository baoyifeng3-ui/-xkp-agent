package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"xkp-agent/internal/config"
	"xkp-agent/internal/identity"
)

type Client struct {
	baseURL string
	http    *http.Client
}
type Enrollment struct {
	AgentID    string `json:"agentId"`
	Credential string `json:"credential"`
}

func New(cfg config.Config) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if !cfg.Development {
		pemData, err := os.ReadFile(cfg.CACertificate)
		if err != nil {
			return nil, fmt.Errorf("read CA certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("CA certificate is invalid")
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	return &Client{baseURL: strings.TrimRight(cfg.ManagementURL, "/"), http: &http.Client{Transport: transport, Timeout: 15 * time.Second}}, nil
}

func (c *Client) Enroll(ctx context.Context, token string, info identity.Info) (Enrollment, error) {
	payload := struct {
		Token         string `json:"token"`
		DisplayName   string `json:"displayName"`
		MachineDigest string `json:"machineDigest"`
		Hostname      string `json:"hostname"`
		PrimaryIP     string `json:"primaryIp"`
		MACAddress    string `json:"macAddress"`
		AgentVersion  string `json:"agentVersion"`
	}{token, info.Hostname, info.MachineDigest, info.Hostname, info.PrimaryIP, info.MACAddress, "0.1.0"}
	body, err := json.Marshal(payload)
	if err != nil {
		return Enrollment{}, fmt.Errorf("encode enrollment request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/agent/v1/register", bytes.NewReader(body))
	if err != nil {
		return Enrollment{}, fmt.Errorf("create enrollment request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Enrollment{}, fmt.Errorf("enrollment request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return Enrollment{}, fmt.Errorf("enrollment rejected with status %d", resp.StatusCode)
	}
	var result Enrollment
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return Enrollment{}, fmt.Errorf("decode enrollment response")
	}
	if result.AgentID == "" || result.Credential == "" {
		return Enrollment{}, fmt.Errorf("enrollment response is incomplete")
	}
	return result, nil
}
