package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ManagementURL            string `yaml:"managementUrl"`
	CACertificate            string `yaml:"caCertificate"`
	WorkspacePath            string `yaml:"workspacePath"`
	EnvironmentWorkspaceRoot string `yaml:"environmentWorkspaceRoot,omitempty"`
	AgentID                  string `yaml:"agentId,omitempty"`
	Credential               string `yaml:"credential,omitempty"`
	Development              bool   `yaml:"development,omitempty"`
}

func Load(path string) (Config, error) {
	var cfg Config
	f, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open configuration: %w", err)
	}
	defer f.Close()
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode configuration: %w", err)
	}
	if cfg.CACertificate != "" && !filepath.IsAbs(cfg.CACertificate) {
		cfg.CACertificate = filepath.Join(filepath.Dir(path), cfg.CACertificate)
	}
	if cfg.EnvironmentWorkspaceRoot == "" {
		cfg.EnvironmentWorkspaceRoot = filepath.Join(cfg.WorkspacePath, "environments")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if cfg.Credential != "" {
		info, err := os.Stat(path)
		if err != nil {
			return Config{}, err
		}
		if info.Mode().Perm()&0077 != 0 {
			return Config{}, fmt.Errorf("credential file permissions must be 0600")
		}
	}
	return cfg, nil
}

func (c Config) Validate() error {
	u, err := url.Parse(c.ManagementURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid management URL")
	}
	if !c.Development && u.Scheme != "https" {
		return fmt.Errorf("production management URL must use HTTPS")
	}
	if c.Development && u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("invalid management URL scheme")
	}
	if !filepath.IsAbs(c.WorkspacePath) {
		return fmt.Errorf("workspace path must be absolute")
	}
	if !filepath.IsAbs(c.EnvironmentWorkspaceRoot) {
		return fmt.Errorf("environment workspace root must be absolute")
	}
	if !c.Development {
		if c.CACertificate == "" {
			return fmt.Errorf("CA certificate is required")
		}
		if _, err := os.ReadFile(c.CACertificate); err != nil {
			return fmt.Errorf("read CA certificate: %w", err)
		}
	}
	if (c.AgentID == "") != (c.Credential == "") {
		return fmt.Errorf("agent ID and credential must be stored together")
	}
	return nil
}

func Save(path string, cfg Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".xkp-agent-credentials-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}
