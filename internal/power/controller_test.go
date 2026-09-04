package power

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type recordingRunner struct {
	name string
	args []string
	err  error
}

type sequenceRunner struct { calls []string; failures int }
func (r *sequenceRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if len(r.calls) <= r.failures { return errors.New("exit status 1") }
	return nil
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
	runner := &sequenceRunner{failures: 4}
	if err := NewLinuxController(runner).Shutdown(context.Background()); err == nil {
		t.Fatal("expected poweroff failure")
	}
}

func TestLinuxControllerFallsBackToSystemctl(t *testing.T) {
	runner := &sequenceRunner{failures: 1}
	if err := NewLinuxController(runner).Shutdown(context.Background()); err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(runner.calls, []string{"/usr/bin/loginctl poweroff", "/usr/bin/systemctl poweroff"}) {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestUnsupportedControllerFailsClosed(t *testing.T) {
	if err := (unsupportedController{}).Shutdown(context.Background()); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("error = %v", err)
	}
}
