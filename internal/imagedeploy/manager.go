package imagedeploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"xkp-agent/internal/protocol"
)

const maxArchiveBytes int64 = 50 * 1024 * 1024 * 1024

var ErrImageExists = errors.New("镜像已存在，请确认覆盖后重试")

type Downloader interface {
	DownloadImageArchive(context.Context, string, int64) (io.ReadCloser, int64, error)
}

type Manager struct {
	downloader Downloader
}

var dockerCommand = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

func NewManager(downloader Downloader) *Manager {
	if downloader == nil {
		panic("image archive downloader is required")
	}
	return &Manager{downloader: downloader}
}

func (m *Manager) Deploy(ctx context.Context, payload protocol.ImageDeploymentPayload,
	progress func(int64, int64, string) error) (map[string]interface{}, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Hour)
	defer cancel()
	target := payload.ImageName
	if target == "" {
		target = payload.RegistryDigest
	}
	if present, err := imagePresent(ctx, target); err != nil {
		return nil, err
	} else if present && !payload.Overwrite {
		return nil, ErrImageExists
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
	output, err := dockerCommand(ctx, "load", "--input", path)
	if err != nil {
		return nil, fmt.Errorf("Docker 镜像导入失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	loaded := parseLoadedImage(string(output))
	if loaded == "" {
		return nil, fmt.Errorf("镜像归档必须包含一个明确的镜像，当前结果为空或包含多个镜像")
	}
	if loaded != target {
		if tagOutput, tagErr := dockerCommand(ctx, "tag", loaded, target); tagErr != nil {
			return nil, fmt.Errorf("镜像标签转换失败: %w: %s", tagErr, strings.TrimSpace(string(tagOutput)))
		}
	}
	inspectOutput, inspectErr := dockerCommand(ctx, "image", "inspect", "--format", "{{.Id}}", target)
	if inspectErr != nil {
		return nil, fmt.Errorf("Docker 镜像检查失败: %w: %s", inspectErr, strings.TrimSpace(string(inspectOutput)))
	}
	imageID := strings.TrimSpace(string(inspectOutput))
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(imageID) {
		return nil, fmt.Errorf("镜像标签转换后校验失败: %s", target)
	}
	return map[string]interface{}{
		"deploymentId":   payload.DeploymentID,
		"componentType":  payload.ComponentType,
		"registryDigest": payload.RegistryDigest,
		"updatePolicy":   payload.UpdatePolicy,
		"loadedBytes":    written,
		"dockerOutput":   string(output),
		"imageId":        imageID,
	}, nil
}

var loadedImagePattern = regexp.MustCompile(`(?m)Loaded image:\s*([^\r\n]+)`)
var loadedImageIDPattern = regexp.MustCompile(`(?m)Loaded image ID:\s*([^\r\n]+)`)

func parseLoadedImage(output string) string {
	matches := append(loadedImagePattern.FindAllStringSubmatch(output, -1), loadedImageIDPattern.FindAllStringSubmatch(output, -1)...)
	if len(matches) != 1 {
		return ""
	}
	return strings.TrimSpace(matches[0][1])
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
	if output, err := dockerCommand(ctx, "image", "inspect", digest); err != nil {
		if strings.Contains(string(output), "No such image:") {
			return false, nil
		}
		return false, fmt.Errorf("Docker 镜像检查失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return true, nil
}
