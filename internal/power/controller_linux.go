//go:build linux

package power

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type linuxController struct {
	runner CommandRunner
}

func NewController() Controller {
	return NewLinuxController(execRunner{})
}

func NewLinuxController(runner CommandRunner) Controller {
	if runner == nil {
		panic("power command runner is required")
	}
	return &linuxController{runner: runner}
}

func (c *linuxController) Shutdown(ctx context.Context) error {
	commands := []struct {
		name string
		args []string
	}{
		{"/usr/bin/loginctl", []string{"poweroff"}},
		{"/usr/bin/systemctl", []string{"poweroff"}},
		{"/usr/sbin/shutdown", []string{"-h", "now"}},
		{"/sbin/poweroff", nil},
	}
	var failures []string
	for _, command := range commands {
		if err := c.runner.Run(ctx, command.name, command.args...); err == nil {
			return nil
		} else {
			failures = append(failures, command.name+": "+err.Error())
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return fmt.Errorf("all poweroff methods failed: %s", strings.Join(failures, "; "))
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}
