package imagedeploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"xkp-agent/internal/protocol"
)

const maxArchiveBytes int64 = 50 * 1024 * 1024 * 1024

type Downloader interface {
	DownloadImageArchive(context.Context, string, int64) (io.ReadCloser, int64, error)
}

type Manager struct {
	downloader Downloader
}

func NewManager(downloader Downloader) *Manager {
	if downloader == nil {
		panic("image archive downloader is required")
	}
	return &Manager{downloader: downloader}
}

func (m *Manager) Deploy(ctx context.Context, payload protocol.ImageDeploymentPayload,
	progress func(int64, int64, string) error) (map[string]interface{}, error) {
	if present, err := imagePresent(ctx, payload.RegistryDigest); err != nil {
		return nil, err
	} else if present {
		return map[string]interface{}{
			"deploymentId": payload.DeploymentID, "componentType": payload.ComponentType,
			"registryDigest": payload.RegistryDigest, "alreadyPresent": true,
		}, nil
	}
	archive, totalBytes, err := m.downloader.DownloadImageArchive(ctx, payload.DeploymentID, maxArchiveBytes)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	tempDir, err := os.MkdirTemp("", "xkp-image-deployment-")
	if err != nil {
		return nil, fmt.Errorf("create image staging directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	path := filepath.Join(tempDir, "image.tar")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("create image archive: %w", err)
	}
	written, copyErr := copyWithProgress(file, io.LimitReader(archive, maxArchiveBytes+1),
		totalBytes, progress)
	closeErr := file.Close()
	if copyErr != nil {
		return nil, fmt.Errorf("download image archive: %w", copyErr)
	}
	if closeErr != nil || written <= 0 || written > maxArchiveBytes {
		return nil, fmt.Errorf("downloaded image archive size is invalid")
	}
	if progress != nil {
		_ = progress(written, totalBytes, "IMPORTING")
	}
	command := exec.CommandContext(ctx, "docker", "load", "--input", path)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker load failed: %s", string(output))
	}
	return map[string]interface{}{
		"deploymentId":   payload.DeploymentID,
		"componentType":  payload.ComponentType,
		"registryDigest": payload.RegistryDigest,
		"updatePolicy":   payload.UpdatePolicy,
		"loadedBytes":    written,
		"dockerOutput":   string(output),
	}, nil
}

func copyWithProgress(dst io.Writer, src io.Reader, total int64,
	progress func(int64, int64, string) error) (int64, error) {
	buffer := make([]byte, 1024*1024)
	var written, lastReported int64
	for {
		count, readErr := src.Read(buffer)
		if count > 0 {
			n, writeErr := dst.Write(buffer[:count])
			written += int64(n)
			if writeErr != nil || n != count {
				if writeErr == nil {
					writeErr = io.ErrShortWrite
				}
				return written, writeErr
			}
			if progress != nil && (written-lastReported >= 64*1024*1024 || written == total) {
				if err := progress(written, total, "DOWNLOADING"); err != nil {
					return written, err
				}
				lastReported = written
			}
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

func imagePresent(ctx context.Context, digest string) (bool, error) {
	command := exec.CommandContext(ctx, "docker", "image", "ls", "--digests", "--format", "{{.Digest}}")
	output, err := command.Output()
	if err != nil {
		return false, fmt.Errorf("inspect local images: %w", err)
	}
	for _, value := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(value) == digest {
			return true, nil
		}
	}
	return false, nil
}
