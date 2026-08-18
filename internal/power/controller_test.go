package power

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type recordingRunner struct {
	name string
	args []string
	err  error
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.name = name
	r.args = append([]string(nil), args...)
	return r.err
}

func TestLinuxControllerRunsExactSystemctlPoweroff(t *testing.T) {
	runner := &recordingRunner{}
	controller := NewLinuxController(runner)

	if err := controller.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.name != "/usr/bin/loginctl" || !reflect.DeepEqual(runner.args, []string{"poweroff"}) {
		t.Fatalf("command = %q %v", runner.name, runner.args)
	}
}

func TestLinuxControllerPropagatesRunnerFailure(t *testing.T) {
	runner := &recordingRunner{err: errors.New("permission denied")}
	if err := NewLinuxController(runner).Shutdown(context.Background()); err == nil {
		t.Fatal("expected poweroff failure")
	}
}

func TestUnsupportedControllerFailsClosed(t *testing.T) {
	if err := (unsupportedController{}).Shutdown(context.Background()); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("error = %v", err)
	}
}
