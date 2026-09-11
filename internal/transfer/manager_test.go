package transfer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xkp-agent/internal/protocol"
)

type testDownloader struct{}

func (testDownloader) Download(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("hello")), nil
}

func TestTransferWritesVerifiedContentAndPreservesExistingOnChecksumFailure(t *testing.T) {
	root := t.TempDir()
	m := New(testDownloader{}, root)
	p := protocol.FileTransferPayload{TargetRelativePath: "datasets/data.txt", SHA256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}
	if err := m.Transfer(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	p.SHA256 = strings.Repeat("0", 64)
	if err := m.Transfer(context.Background(), p); err == nil {
		t.Fatal("bad checksum accepted")
	}
	content, err := os.ReadFile(filepath.Join(root, "datasets/data.txt"))
	if err != nil || string(content) != "hello" {
		t.Fatalf("content=%q error=%v", content, err)
	}
	parts, _ := filepath.Glob(filepath.Join(root, "datasets/*.part*"))
	if len(parts) != 0 {
		t.Fatal("partial files leaked")
	}
}

func TestTransferRejectsOutsidePathsAndSymlinkParents(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "datasets")); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"../outside/data.txt", "datasets/data.txt"} {
		p := protocol.FileTransferPayload{TargetRelativePath: target, SHA256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}
		if err := New(testDownloader{}, root).Transfer(context.Background(), p); err == nil {
			t.Errorf("unsafe path accepted: %s", target)
		}
	}
}
