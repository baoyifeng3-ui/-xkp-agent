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
	"regexp"
	"strings"
	"time"

	"xkp-agent/internal/config"
	"xkp-agent/internal/identity"
	"xkp-agent/internal/protocol"
	"xkp-agent/internal/runtime"
)

type Client struct {
	baseURL    string
	agentID    string
	credential string
	http       *http.Client
	now        func() time.Time
}

type TerminalTicket struct {
	Ticket    string
	ExpiresAt time.Time
}

var canonicalTerminalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var terminalTicketPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

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
	return &Client{baseURL: strings.TrimRight(cfg.ManagementURL, "/"), agentID: cfg.AgentID,
		credential: cfg.Credential, http: &http.Client{Transport: transport, Timeout: 35 * time.Second},
		now: func() time.Time { return time.Now().UTC() }}, nil
}

func (c *Client) ExchangeTerminalTicket(ctx context.Context, sessionID, commandID, leaseToken string) (TerminalTicket, error) {
	if !canonicalTerminalUUID.MatchString(sessionID) || !canonicalTerminalUUID.MatchString(commandID) ||
		!canonicalTerminalUUID.MatchString(leaseToken) {
		return TerminalTicket{}, fmt.Errorf("terminal ticket identifiers are invalid")
	}
	payload := struct {
		CommandID  string `json:"commandId"`
		LeaseToken string `json:"leaseToken"`
	}{CommandID: commandID, LeaseToken: leaseToken}
	body, err := json.Marshal(payload)
	if err != nil {
		return TerminalTicket{}, fmt.Errorf("encode terminal ticket request")
	}
	path := "/agent/v1/terminal-sessions/" + sessionID + "/agent-ticket"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return TerminalTicket{}, fmt.Errorf("create terminal ticket request")
	}
	if c.agentID == "" || c.credential == "" {
		return TerminalTicket{}, fmt.Errorf("agent is not enrolled")
	}
	req.Header.Set("Authorization", "Bearer "+c.credential)
	req.Header.Set("Content-Type", "application/json")
	requestClient := *c.http
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := requestClient.Do(req)
	if err != nil {
		return TerminalTicket{}, fmt.Errorf("terminal ticket request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.CopyN(io.Discard, resp.Body, 4097)
		return TerminalTicket{}, fmt.Errorf("terminal ticket request rejected with status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(data) == 0 || len(data) > 4096 {
		return TerminalTicket{}, fmt.Errorf("terminal ticket response size is invalid")
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return TerminalTicket{}, fmt.Errorf("terminal ticket response is invalid")
	}
	var raw struct {
		Ticket    string `json:"ticket"`
		ExpiresAt string `json:"expiresAt"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return TerminalTicket{}, fmt.Errorf("terminal ticket response is invalid")
	}
	if !terminalTicketPattern.MatchString(raw.Ticket) || len(raw.ExpiresAt) != len("2006-01-02T15:04:05Z") {
		return TerminalTicket{}, fmt.Errorf("terminal ticket response is invalid")
	}
	expiresAt, err := time.Parse("2006-01-02T15:04:05Z", raw.ExpiresAt)
	if err != nil || expiresAt.Format("2006-01-02T15:04:05Z") != raw.ExpiresAt {
		return TerminalTicket{}, fmt.Errorf("terminal ticket response is invalid")
	}
	now := time.Now().UTC()
	if c.now != nil {
		now = c.now().UTC()
	}
	if !expiresAt.After(now) || expiresAt.After(now.Add(time.Minute)) {
		return TerminalTicket{}, fmt.Errorf("terminal ticket response expiry is invalid")
	}
	return TerminalTicket{Ticket: raw.Ticket, ExpiresAt: expiresAt}, nil
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("invalid object key")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate object key")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("invalid JSON delimiter")
		}
	}
	return walk()
}

func (c *Client) Enroll(ctx context.Context, token string, info identity.Info) (Enrollment, error) {
	return c.EnrollWithDisplayName(ctx, token, info.Hostname, info)
}

func (c *Client) EnrollWithDisplayName(ctx context.Context, token, displayName string, info identity.Info) (Enrollment, error) {
	payload := struct {
		Token         string `json:"token"`
		DisplayName   string `json:"displayName"`
		MachineDigest string `json:"machineDigest"`
		Hostname      string `json:"hostname"`
		PrimaryIP     string `json:"primaryIp"`
		MACAddress    string `json:"macAddress"`
		AgentVersion  string `json:"agentVersion"`
	}{token, displayName, info.MachineDigest, info.Hostname, info.PrimaryIP, info.MACAddress, "0.1.0"}
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

func (c *Client) Heartbeat(ctx context.Context, request runtime.HeartbeatRequest) (runtime.HeartbeatAck, error) {
	var ack runtime.HeartbeatAck
	if err := c.authenticatedJSON(ctx, http.MethodPost, "/agent/v1/heartbeat", request, &ack); err != nil {
		return ack, err
	}
	return ack, nil
}

func (c *Client) PollCommands(ctx context.Context, waitSeconds int) ([]json.RawMessage, error) {
	if waitSeconds < 0 || waitSeconds > 25 {
		return nil, fmt.Errorf("invalid command poll wait")
	}
	var response struct {
		Commands []json.RawMessage `json:"commands"`
	}
	path := fmt.Sprintf("/agent/v1/commands/poll?waitSeconds=%d", waitSeconds)
	if err := c.authenticatedJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Commands, nil
}

func (c *Client) StartCommand(ctx context.Context, commandID, leaseToken string) error {
	payload := struct {
		LeaseToken string `json:"leaseToken"`
	}{LeaseToken: leaseToken}
	var response struct {
		CommandID string `json:"commandId"`
		AgentID   string `json:"agentId"`
		State     string `json:"state"`
	}
	path := "/agent/v1/commands/" + commandID + "/start"
	if err := c.authenticatedJSON(ctx, http.MethodPost, path, payload, &response); err != nil {
		return err
	}
	if response.CommandID != commandID || response.AgentID != c.agentID || response.State != "RUNNING" {
		return fmt.Errorf("command acknowledgement is invalid")
	}
	return nil
}

func (c *Client) FinishCommand(ctx context.Context, commandID string, result protocol.CommandResult) error {
	var response map[string]interface{}
	path := "/agent/v1/commands/" + commandID + "/result"
	return c.authenticatedJSON(ctx, http.MethodPost, path, result, &response)
}

func (c *Client) authenticatedJSON(ctx context.Context, method, path string, payload, result interface{}) error {
	if c.agentID == "" || c.credential == "" {
		return fmt.Errorf("agent is not enrolled")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode agent request")
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create agent request")
	}
	req.Header.Set("Authorization", "Bearer "+c.credential)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("agent request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("agent request rejected with status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
		return fmt.Errorf("decode agent response")
	}
	return nil
}
