//go:build !linux

package terminal

import (
	"context"
	"testing"
	"xkp-agent/internal/protocol"
)

func TestUnsupportedRunnerReturnsStableError(t *testing.T) {
	r := &unsupportedRunner{}
	if _, err := r.Start(context.Background(), protocol.Command{}); err == nil || err.Error() != "TERMINAL_UNSUPPORTED_PLATFORM" {
		t.Fatalf("error = %v", err)
	}
}
