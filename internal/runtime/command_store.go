package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"xkp-agent/internal/protocol"
)

type StoredCommand struct {
	CommandID  string                  `json:"commandId"`
	LeaseToken string                  `json:"leaseToken"`
	Result     *protocol.CommandResult `json:"result,omitempty"`
}

type CommandStore interface {
	Load() (*StoredCommand, error)
	Save(StoredCommand) error
	Clear() error
}

type FileCommandStore struct{ path string }

func NewFileCommandStore(path string) CommandStore {
	if path == "" || !filepath.IsAbs(path) {
		panic("command store path must be absolute")
	}
	return &FileCommandStore{path: path}
}

func (s *FileCommandStore) Load() (*StoredCommand, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read command state: %w", err)
	}
	if len(data) == 0 || len(data) > 64*1024 {
		return nil, fmt.Errorf("command state size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state StoredCommand
	if err := decoder.Decode(&state); err != nil || state.CommandID == "" || state.LeaseToken == "" {
		return nil, fmt.Errorf("command state is invalid")
	}
	return &state, nil
}

func (s *FileCommandStore) Save(state StoredCommand) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode command state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("create command state directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".xkp-command-*")
	if err != nil {
		return fmt.Errorf("create command state: %w", err)
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
	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}
	return os.Chmod(s.path, 0600)
}

func (s *FileCommandStore) Clear() error {
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
