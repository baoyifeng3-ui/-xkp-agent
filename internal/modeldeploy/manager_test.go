package modeldeploy

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"xkp-agent/internal/protocol"
)

type runnerStub struct {
	output string
	cmd    []string
}

func (r *runnerStub) Run(_ context.Context, _ string, command []string) (string, error) {
	r.cmd = command
	return r.output, nil
}

func TestListReturnsRelativeStudentFiles(t *testing.T) {
	runner := &runnerStub{output: "/home/student/models/a.onnx\n/home/student/config/a.json\n"}
	files, err := New(runner).List(context.Background(), "editor")
	if err != nil || !reflect.DeepEqual(files, []string{"config/a.json", "models/a.onnx"}) {
		t.Fatalf("files=%v error=%v", files, err)
	}
}

func TestDeployCopiesSelectedFilesToT100Directory(t *testing.T) {
	runner := &runnerStub{}
	err := New(runner).Deploy(context.Background(), protocol.ModelWorkspacePayload{
		ContainerName: "editor", ModelPath: "models/a model.onnx", ConfigPath: "config/a.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "mkdir -p '/usr/local/zy-T100/utils_x86/models/A' && test ! -e '/usr/local/zy-T100/utils_x86/models/A/a model.onnx' && test ! -e '/usr/local/zy-T100/utils_x86/a.json' && cp -- '/home/student/models/a model.onnx' '/usr/local/zy-T100/utils_x86/models/A/a model.onnx' && cp -- '/home/student/config/a.json' '/usr/local/zy-T100/utils_x86/a.json'"
	if len(runner.cmd) != 3 || runner.cmd[2] != want {
		t.Fatalf("command=%q", runner.cmd)
	}
}

func TestDeployMakesObjectDetectorImportableByT100(t *testing.T) {
	runner := &runnerStub{}
	err := New(runner).Deploy(context.Background(), protocol.ModelWorkspacePayload{
		ContainerName: "editor", ModelPath: "model/detection.tflite", ConfigPath: "model/object_detector.py",
		Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.cmd[2], "'/usr/local/zy-T100/utils_x86/object_detector.py'") {
		t.Fatalf("command=%q", runner.cmd)
	}
}
