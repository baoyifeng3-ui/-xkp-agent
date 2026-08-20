//go:build !linux

package terminal

import (
	"context"
	"xkp-agent/internal/protocol"
)

type unsupportedRunner struct{}
type ticketClient interface{}

func NewRunner(ticketClient, bool) Runner { return unsupportedRunner{} }
func (unsupportedRunner) Start(context.Context, protocol.Command) (<-chan error, error) {
	return nil, &Error{Code: "TERMINAL_UNSUPPORTED_PLATFORM"}
}
