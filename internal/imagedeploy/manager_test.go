package imagedeploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"xkp-agent/internal/protocol"
)

type archiveDownloader struct{}

func (archiveDownloader) DownloadImageArchive(context.Context, string, int64) (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader([]byte("tar"))), 3, nil
}

func TestDeployRetagsLoadedArchiveToRequestedImageName(t *testing.T) {
	previous := dockerCommand
	defer func() { dockerCommand = previous }()
	var commands [][]string
	inspects := 0
	dockerCommand = func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, args)
		switch args[0] {
		case "load":
			return []byte("Loaded image: zy-anno:latest\n"), nil
		case "image":
			inspects++
			if inspects == 1 {
				return []byte("Error response from daemon: No such image: xkp/anno:v1"), io.ErrUnexpectedEOF
			}
			return []byte("sha256:" + strings.Repeat("a", 64) + "\n"), nil
		case "tag":
			return nil, nil
		default:
			t.Fatalf("unexpected docker command: %v", args)
			return nil, nil
		}
	}

	result, err := NewManager(archiveDownloader{}).Deploy(context.Background(), protocol.ImageDeploymentPayload{
		DeploymentID: "deployment-1", ImageName: "xkp/anno:v1", ComponentType: "DIRECT",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result["deploymentId"] != "deployment-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result["imageId"] != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("missing actual image ID: %v", result)
	}
	if len(commands) != 4 || commands[2][0] != "tag" || commands[2][1] != "zy-anno:latest" || commands[2][2] != "xkp/anno:v1" {
		t.Fatalf("expected load, tag, inspect sequence; got %#v", commands)
	}
}

func TestParseLoadedImage(t *testing.T) {
	if got := parseLoadedImage("Loaded image: one:latest\nLoaded image: two:latest\n"); got != "" {
		t.Fatalf("ambiguous archive picked %q", got)
	}
	if got := parseLoadedImage("Loaded image: xkp/anno:v1\n"); got != "xkp/anno:v1" {
		t.Fatalf("got %q", got)
	}
	if got := parseLoadedImage("Loaded image ID: sha256:abc\n"); got != "sha256:abc" {
		t.Fatalf("got %q", got)
	}
}

func TestDeployDuplicateAndDockerFailure(t *testing.T) {
	previous := dockerCommand
	defer func() { dockerCommand = previous }()
	dockerCommand = func(context.Context, ...string) ([]byte, error) { return nil, nil }
	_, err := NewManager(archiveDownloader{}).Deploy(context.Background(), protocol.ImageDeploymentPayload{ImageName: "xkp/anno:latest"}, nil)
	if !errors.Is(err, ErrImageExists) {
		t.Fatalf("duplicate error: %v", err)
	}
	dockerCommand = func(context.Context, ...string) ([]byte, error) {
		return []byte("Cannot connect to the Docker daemon"), io.ErrUnexpectedEOF
	}
	if _, err := imagePresent(context.Background(), "xkp/anno:latest"); err == nil {
		t.Fatal("daemon error was swallowed")
	}
}
