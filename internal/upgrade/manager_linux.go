//go:build linux

package upgrade

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"xkp-agent/internal/protocol"
)

const maxBinaryBytes int64 = 64 * 1024 * 1024

type Downloader interface {
	DownloadUpgradeBinary(context.Context, string, int64) (io.ReadCloser, error)
}

type Manager struct { downloader Downloader; executable string }

func NewManager(downloader Downloader) (*Manager, error) {
	executable, err := os.Executable()
	if err != nil { return nil, fmt.Errorf("resolve Agent executable: %w", err) }
	return &Manager{downloader: downloader, executable: executable}, nil
}

func (m *Manager) Prepare(ctx context.Context, payload protocol.UpgradePayload) error {
	body, err := m.downloader.DownloadUpgradeBinary(ctx, payload.DownloadPath, maxBinaryBytes)
	if err != nil { return err }
	defer body.Close()
	temporary := "/etc/xkp-agent/xkp-agent.new"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil { return fmt.Errorf("create upgrade binary: %w", err) }
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, maxBinaryBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written <= 0 || written > maxBinaryBytes || fmt.Sprintf("%x", hash.Sum(nil)) != payload.SHA256 {
		_ = os.Remove(temporary)
		return fmt.Errorf("upgrade binary verification failed")
	}
	return os.Chmod(temporary, 0755)
}

func (m *Manager) Activate() error {
	backup := "/etc/xkp-agent/xkp-agent.backup"
	temporary := "/etc/xkp-agent/xkp-agent.new"
	script := filepath.Join("/etc/xkp-agent", ".xkp-agent-upgrade.sh")
	content := fmt.Sprintf(`#!/bin/sh
set -eu
cp %q %q
mv %q %q
chmod 0755 %q
systemctl restart xkp-agent.service
sleep 8
if ! systemctl is-active --quiet xkp-agent.service; then
  cp %q %q
  systemctl restart xkp-agent.service
  exit 1
fi
rm -f %q %q
`, m.executable, backup, temporary, m.executable, m.executable, backup, m.executable, backup, script)
	if err := os.WriteFile(script, []byte(content), 0700); err != nil { return err }
	return exec.Command("/usr/bin/systemd-run", "--unit=xkp-agent-upgrade", "--on-active=2s", script).Run()
}
