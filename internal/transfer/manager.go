package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"xkp-agent/internal/protocol"
)

type downloader interface {
	Download(context.Context, string) (io.ReadCloser, error)
}
type Manager struct {
	client downloader
	root   string
}

func New(client downloader, root string) *Manager { return &Manager{client: client, root: root} }
func (m *Manager) Transfer(ctx context.Context, p protocol.FileTransferPayload) error {
	target := filepath.Join(m.root, filepath.FromSlash(p.TargetRelativePath))
	rel, err := filepath.Rel(m.root, target)
	if err != nil || rel == ".." || filepath.IsAbs(rel) {
		return fmt.Errorf("target path is invalid")
	}
	if err = os.MkdirAll(filepath.Dir(target), 0750); err != nil {
		return err
	}
	body, err := m.client.Download(ctx, p.DownloadPath)
	if err != nil {
		return err
	}
	defer body.Close()
	tmp := target + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, hash), body)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(tmp)
		return fmt.Errorf("write transfer failed")
	}
	if hex.EncodeToString(hash.Sum(nil)) != p.SHA256 {
		os.Remove(tmp)
		return fmt.Errorf("transfer checksum mismatch")
	}
	return os.Rename(tmp, target)
}
