package power

import (
	"context"
	"errors"
)

var ErrUnsupportedPlatform = errors.New("power control is unsupported on this platform")

type Controller interface {
	Shutdown(context.Context) error
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type unsupportedController struct{}

func (unsupportedController) Shutdown(context.Context) error {
	return ErrUnsupportedPlatform
}
