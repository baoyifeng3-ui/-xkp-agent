package terminal

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

const stableSubprotocol = "xkp-terminal-v1"
const maxRelayFrame = 64 << 10

func validateRelayURL(raw, session string, development bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" {
		return fmt.Errorf("invalid relay URL")
	}
	if development && u.Scheme == "ws" {
		host, _, splitErr := net.SplitHostPort(u.Host)
		if splitErr != nil {
			host = u.Hostname()
		}
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			return fmt.Errorf("invalid development relay URL")
		}
	} else if u.Scheme != "wss" {
		return fmt.Errorf("relay URL must use WSS")
	}
	if session == "" {
		return fmt.Errorf("invalid session")
	}
	return nil
}

func relayPath(raw, session string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + session
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}
