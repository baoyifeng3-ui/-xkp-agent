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
