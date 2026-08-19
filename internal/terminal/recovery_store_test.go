package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFileRecoveryStorePersistsOnlyMinimalPrivateState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "terminal-recovery.json")
	store := NewFileRecoveryStore(path)
	recovery := validRecovery()
	if err := store.Save(recovery); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"absoluteExpiresAt", "agentConnectionDeadline", "commandId", "leaseToken", "sessionId"}
	gotKeys := make([]string, 0, len(raw))
	for key := range raw {
		gotKeys = append(gotKeys, key)
	}
	sortStrings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("keys = %v", gotKeys)
	}
	for _, forbidden := range []string{"ticket", "relayUrl", "credential", "browser", "terminal", "shell", "environment"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("stored forbidden content %q: %s", forbidden, data)
		}
	}
	info, _ := os.Stat(path)
	dirInfo, _ := os.Stat(filepath.Dir(path))
	if info.Mode().Perm() != 0600 || dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("modes file=%o dir=%o", info.Mode().Perm(), dirInfo.Mode().Perm())
	}
	loaded, err := store.Load()
	if err != nil || loaded == nil || !reflect.DeepEqual(*loaded, recovery) {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestFileRecoveryStoreRejectsCorruptUnsafeOrNoncanonicalState(t *testing.T) {
	valid := `{"sessionId":"44444444-4444-4444-8444-444444444444","commandId":"77777777-7777-4777-8777-777777777777","leaseToken":"66666666-6666-4666-8666-666666666666","agentConnectionDeadline":"2026-08-20T10:01:30Z","absoluteExpiresAt":"2026-08-20T12:00:00Z"}`
	cases := []string{
		strings.Replace(valid, `}`, `,"ticket":"secret"}`, 1),
		strings.Replace(valid, `"commandId":`, `"commandId":"77777777-7777-4777-8777-777777777777","commandId":`, 1),
		valid + `{}`,
		strings.Replace(valid, "2026-08-20T10:01:30Z", "2026-08-20T10:01:30.000Z", 1),
		strings.Replace(valid, "44444444-4444-4444-8444-444444444444", "../../etc/passwd", 1),
		strings.Replace(valid, "2026-08-20T12:00:00Z", "2026-08-20T10:01:30Z", 1),
		strings.Repeat(" ", maxRecoveryBytes+1),
	}
	for _, data := range cases {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err := NewFileRecoveryStore(path).Load()
		if err == nil || loaded != nil {
			t.Fatalf("accepted state %q", data)
		}
	}
}

func TestFileRecoveryStoreRejectsInsecureExistingFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	data, err := json.Marshal(validRecovery())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if loaded, err := NewFileRecoveryStore(path).Load(); err == nil || loaded != nil {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestFileRecoveryStoreClearIsIdempotentAndRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "state.json")
	if err := os.WriteFile(target, []byte("do not replace"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store := NewFileRecoveryStore(link)
	if err := store.Save(validRecovery()); err == nil {
		t.Fatal("saved through symlink")
	}
	if data, _ := os.ReadFile(target); string(data) != "do not replace" {
		t.Fatalf("target changed: %q", data)
	}
	if err := store.Clear(); err == nil {
		t.Fatal("cleared symlink path")
	}
	os.Remove(link)
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
}

func TestFileRecoveryStoreRejectsSymlinkedParentDirectory(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	linkDir := filepath.Join(dir, "linked")
	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store := NewFileRecoveryStore(filepath.Join(linkDir, "state.json"))
	if err := store.Save(validRecovery()); err == nil {
		t.Fatal("saved through symlinked parent")
	}
	if _, err := os.Stat(filepath.Join(realDir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected target state: %v", err)
	}
}

func TestFileRecoveryStoreAtomicReplaceFailurePreservesPreviousState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := NewFileRecoveryStore(path).(*FileRecoveryStore)
	original := validRecovery()
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}
	store.rename = func(string, string) error { return os.ErrPermission }
	replacement := original
	replacement.SessionID = "55555555-5555-4555-8555-555555555555"
	if err := store.Save(replacement); err == nil {
		t.Fatal("expected replace failure")
	}
	loaded, err := store.Load()
	if err != nil || loaded == nil || !reflect.DeepEqual(*loaded, original) {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".xkp-terminal-recovery-*"))
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestFileRecoveryStoreClearRejectsInsecureParent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := NewFileRecoveryStore(path)
	if err := store.Save(validRecovery()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(); err == nil {
		t.Fatal("cleared recovery through insecure parent")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("recovery removed: %v", err)
	}
}

func TestFileRecoveryStoreClearReportsDirectorySyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewFileRecoveryStore(path).(*FileRecoveryStore)
	if err := store.Save(validRecovery()); err != nil {
		t.Fatal(err)
	}
	store.syncDir = func(string) error { return os.ErrPermission }
	if err := store.Clear(); err == nil {
		t.Fatal("ignored directory sync failure")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state still exists after clear: %v", err)
	}
}

func TestFileRecoveryStoreSaveUsesDeterministicCanonicalFieldOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := NewFileRecoveryStore(path).Save(validRecovery()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"sessionId":"44444444-4444-4444-8444-444444444444","commandId":"77777777-7777-4777-8777-777777777777","leaseToken":"66666666-6666-4666-8666-666666666666","agentConnectionDeadline":"2026-08-20T10:01:30Z","absoluteExpiresAt":"2026-08-20T12:00:00Z"}`
	if string(data) != want {
		t.Fatalf("serialized recovery = %s, want %s", data, want)
	}
}

func TestNewFileRecoveryStoreRequiresAbsolutePath(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("relative path accepted")
		}
	}()
	NewFileRecoveryStore("state.json")
}

func validRecovery() TerminalRecovery {
	return TerminalRecovery{
		SessionID:               "44444444-4444-4444-8444-444444444444",
		CommandID:               "77777777-7777-4777-8777-777777777777",
		LeaseToken:              "66666666-6666-4666-8666-666666666666",
		AgentConnectionDeadline: time.Date(2026, 8, 20, 10, 1, 30, 0, time.UTC),
		AbsoluteExpiresAt:       time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
	}
}

func sortStrings(values []string) {
	for i := range values {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}
