//go:build linux

package terminal

import (
	"context"
	"testing"
	"xkp-agent/internal/protocol"
)

func TestLinuxRunnerRejectsInvalidCommandWithoutStartingProcess(t *testing.T) {
	r := &linuxRunner{}
	if _, err := r.Start(context.Background(), protocol.Command{}); err == nil {
		t.Fatal("expected invalid command error")
	}
}
