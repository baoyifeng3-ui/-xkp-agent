package modeldeploy

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"xkp-agent/internal/protocol"
)

const studentRoot = "/home/student"
const targetRoot = "/usr/local/zy-T100/utils_x86/models/A"
const configTargetRoot = "/usr/local/zy-T100/utils_x86"

type Runner interface {
	Run(context.Context, string, []string) (string, error)
}

type Manager struct{ runner Runner }

func New(runner Runner) *Manager { return &Manager{runner: runner} }

func (m *Manager) List(ctx context.Context, container string) ([]string, error) {
	output, err := m.runner.Run(ctx, container, []string{"find", studentRoot, "-type", "f"})
	if err != nil {
		return nil, err
	}
	files := make([]string, 0)
	for _, file := range strings.Split(output, "\n") {
		if strings.HasPrefix(file, studentRoot+"/") {
			files = append(files, strings.TrimPrefix(file, studentRoot+"/"))
		}
	}
	sort.Strings(files)
	return files, nil
}

func (m *Manager) Deploy(ctx context.Context, p protocol.ModelWorkspacePayload) error {
	modelSource, configSource := path.Join(studentRoot, p.ModelPath), path.Join(studentRoot, p.ConfigPath)
	modelTarget, configTarget := path.Join(targetRoot, path.Base(p.ModelPath)), path.Join(configTargetRoot, path.Base(p.ConfigPath))
	command := "mkdir -p " + quote(targetRoot)
	if !p.Overwrite {
		command += " && test ! -e " + quote(modelTarget) + " && test ! -e " + quote(configTarget)
	}
	force := ""
	if p.Overwrite {
		force = " -f"
	}
	command += " && cp" + force + " -- " + quote(modelSource) + " " + quote(modelTarget)
	command += " && cp" + force + " -- " + quote(configSource) + " " + quote(configTarget)
	if path.Base(p.ConfigPath) == "object_detector.py" {
		command += " && cp" + force + " -- " + quote(configSource) + " " +
			quote("/usr/local/zy-T100/utils_x86/object_detector.py")
	}
	_, err := m.runner.Run(ctx, p.ContainerName, []string{"sh", "-lc", command})
	if err != nil {
		return fmt.Errorf("deploy model files: %w", err)
	}
	return nil
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
