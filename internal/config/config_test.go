package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRejectsUnknownFieldAndInsecureProductionURL(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"unknown":  "managementUrl: https://127.0.0.1:19443\ncaCertificate: ca.crt\nworkspacePath: /srv/xkp\nextra: true\n",
		"insecure": "managementUrl: http://127.0.0.1:19443\ncaCertificate: ca.crt\nworkspacePath: /srv/xkp\n",
	} {
		path := filepath.Join(dir, name+".yaml")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("%s config was accepted", name)
		}
	}
}

func TestSaveWritesCredentialFileWithOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.yaml")
	cfg := Config{ManagementURL: "https://127.0.0.1:19443", CACertificate: "ca.crt", WorkspacePath: "/srv/xkp", AgentID: "agent", Credential: "secret"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permission = %o", info.Mode().Perm())
	}
}

func TestLoadDefaultsEnvironmentWorkspaceBelowAgentWorkspace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "managementUrl: http://127.0.0.1:19443\nworkspacePath: " + filepath.ToSlash(dir) + "\ndevelopment: true\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "environments")
	if cfg.EnvironmentWorkspaceRoot != want {
		t.Fatalf("environment workspace = %q, want %q", cfg.EnvironmentWorkspaceRoot, want)
	}
}

func TestLoadRejectsRelativeEnvironmentWorkspace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "managementUrl: http://127.0.0.1:19443\nworkspacePath: " + filepath.ToSlash(dir) +
		"\nenvironmentWorkspaceRoot: relative/path\ndevelopment: true\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("relative environment workspace was accepted")
	}
}
