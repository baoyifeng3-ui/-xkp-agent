package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	stableSubprotocol   = "xkp-terminal-v1"
	maxRelayFrame       = 64 << 10
	maxControlFrame     = 4 << 10
	maxOutboundBytes    = 1 << 20
	maxOutboundMessages = 64
	tokenRate           = 2 << 20
	tokenBurst          = 8 << 20
)

func validateRelayURL(raw, session string, development bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Hostname() == "" {
		return fmt.Errorf("invalid relay URL")
	}
	if development && u.Scheme == "ws" {
		host := strings.ToLower(u.Hostname())
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			return fmt.Errorf("invalid development relay URL")
		}
	} else if u.Scheme != "wss" {
		return fmt.Errorf("relay URL must use WSS")
	}
	expected := "/terminal/v1/agent/" + session
	if session == "" || u.Path != expected || u.EscapedPath() != expected {
		return fmt.Errorf("invalid relay path")
	}
	return nil
}

func relayPath(raw, session string) (string, error) {
	if err := validateRelayURL(raw, session, strings.HasPrefix(raw, "ws://")); err != nil {
		return "", err
	}
	return raw, nil
}

type control struct {
	kind          string
	columns, rows int
	reason        string
}

func parseControl(data []byte) (control, error) {
	if len(data) == 0 || len(data) > maxControlFrame {
		return control{}, fmt.Errorf("invalid control")
	}
	if err := rejectDuplicateControlFields(data); err != nil {
		return control{}, fmt.Errorf("invalid control")
	}
	var fields map[string]json.RawMessage
	d := json.NewDecoder(bytes.NewReader(data))
	if d.Decode(&fields) != nil || ensureEOF(d) != nil {
		return control{}, fmt.Errorf("invalid control")
	}
	var kind string
	if raw, ok := fields["type"]; !ok || json.Unmarshal(raw, &kind) != nil {
		return control{}, fmt.Errorf("invalid control")
	}
	c := control{kind: kind}
	switch kind {
	case "ping", "pong":
		if len(fields) != 1 {
			return control{}, fmt.Errorf("invalid control")
		}
	case "resize":
		if len(fields) != 3 || json.Unmarshal(fields["columns"], &c.columns) != nil || json.Unmarshal(fields["rows"], &c.rows) != nil || c.columns < 20 || c.columns > 500 || c.rows < 5 || c.rows > 200 {
			return control{}, fmt.Errorf("invalid control")
		}
	case "close":
		if len(fields) != 2 || json.Unmarshal(fields["reason"], &c.reason) != nil || (c.reason != "browser-disconnected" && c.reason != "session-expired" && c.reason != "operator-request") {
			return control{}, fmt.Errorf("invalid control")
		}
	default:
		return control{}, fmt.Errorf("invalid control")
	}
	return c, nil
}

func ensureEOF(d *json.Decoder) error {
	var v any
	if err := d.Decode(&v); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func rejectDuplicateControlFields(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return fmt.Errorf("object required")
	}
	seen := map[string]bool{}
	for d.More() {
		k, err := d.Token()
		if err != nil {
			return err
		}
		s, ok := k.(string)
		if !ok || seen[s] {
			return fmt.Errorf("duplicate")
		}
		seen[s] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return fmt.Errorf("invalid")
		}
	}
	_, err = d.Token()
	return err
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newTokenBucket(now time.Time) tokenBucket { return tokenBucket{tokens: tokenBurst, last: now} }
func (b *tokenBucket) allow(n int, now time.Time) bool {
	if n < 0 {
		return false
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * tokenRate
		if b.tokens > tokenBurst {
			b.tokens = tokenBurst
		}
		b.last = now
	}
	if float64(n) > b.tokens {
		return false
	}
	b.tokens -= float64(n)
	return true
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}
