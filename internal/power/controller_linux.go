//go:build linux

package power

import (
	"context"
	"os/exec"
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
	return c.runner.Run(ctx, "/usr/bin/loginctl", "poweroff")
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}
