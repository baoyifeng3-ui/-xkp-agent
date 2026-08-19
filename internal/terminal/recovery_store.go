package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const maxRecoveryBytes = 4096

var canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var canonicalTimestampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

type TerminalRecovery struct {
	SessionID               string    `json:"sessionId"`
	CommandID               string    `json:"commandId"`
	LeaseToken              string    `json:"leaseToken"`
	AgentConnectionDeadline time.Time `json:"agentConnectionDeadline"`
	AbsoluteExpiresAt       time.Time `json:"absoluteExpiresAt"`
}

type RecoveryStore interface {
	Load() (*TerminalRecovery, error)
	Save(TerminalRecovery) error
	Clear() error
}

type FileRecoveryStore struct {
	path   string
	rename func(string, string) error
}

func NewFileRecoveryStore(path string) RecoveryStore {
	if path == "" || !filepath.IsAbs(path) {
		panic("terminal recovery path must be absolute")
	}
	return &FileRecoveryStore{path: filepath.Clean(path), rename: os.Rename}
}

func (s *FileRecoveryStore) Load() (*TerminalRecovery, error) {
	if err := rejectSymlinkedParents(s.path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect terminal recovery: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("terminal recovery path is unsafe")
	}
	if parent, err := os.Stat(filepath.Dir(s.path)); err != nil || parent.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("terminal recovery directory is unsafe")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read terminal recovery: %w", err)
	}
	if len(data) == 0 || len(data) > maxRecoveryBytes {
		return nil, fmt.Errorf("terminal recovery size is invalid")
	}
	if err := rejectDuplicateFields(data); err != nil {
		return nil, fmt.Errorf("terminal recovery is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var recovery TerminalRecovery
	if err := decoder.Decode(&recovery); err != nil || decoder.Decode(&struct{}{}) != io.EOF || validateRecovery(recovery) != nil {
		return nil, fmt.Errorf("terminal recovery is invalid")
	}
	var rawTimes struct {
		AgentConnectionDeadline string `json:"agentConnectionDeadline"`
		AbsoluteExpiresAt       string `json:"absoluteExpiresAt"`
	}
	if err := json.Unmarshal(data, &rawTimes); err != nil ||
		!canonicalTimestampPattern.MatchString(rawTimes.AgentConnectionDeadline) ||
		!canonicalTimestampPattern.MatchString(rawTimes.AbsoluteExpiresAt) {
		return nil, fmt.Errorf("terminal recovery is invalid")
	}
	return &recovery, nil
}

func (s *FileRecoveryStore) Save(recovery TerminalRecovery) error {
	if err := validateRecovery(recovery); err != nil {
		return err
	}
	data, err := json.Marshal(recovery)
	if err != nil || len(data) > maxRecoveryBytes {
		return fmt.Errorf("encode terminal recovery")
	}
	dir := filepath.Dir(s.path)
	if err := rejectSymlinkedParents(s.path); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create terminal recovery directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("secure terminal recovery directory: %w", err)
	}
	if info, err := os.Lstat(s.path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("terminal recovery path is unsafe")
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect terminal recovery: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".xkp-terminal-recovery-*")
	if err != nil {
		return fmt.Errorf("create terminal recovery: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	fail := func(writeErr error) error {
		_ = tmp.Close()
		return writeErr
	}
	if err := tmp.Chmod(0600); err != nil {
		return fail(fmt.Errorf("secure terminal recovery: %w", err))
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(fmt.Errorf("write terminal recovery: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("sync terminal recovery: %w", err))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close terminal recovery: %w", err)
	}
	rename := s.rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace terminal recovery: %w", err)
	}
	if err := os.Chmod(s.path, 0600); err != nil {
		return fmt.Errorf("secure final terminal recovery: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open terminal recovery directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync terminal recovery directory: %w", err)
	}
	return nil
}

func (s *FileRecoveryStore) Clear() error {
	if err := rejectSymlinkedParents(s.path); err != nil {
		return err
	}
	if parent, err := os.Stat(filepath.Dir(s.path)); err != nil || parent.Mode().Perm() != 0700 {
		return fmt.Errorf("terminal recovery directory is unsafe")
	}
	info, err := os.Lstat(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect terminal recovery: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("terminal recovery path is unsafe")
	}
	if err := os.Remove(s.path); err != nil {
		return fmt.Errorf("clear terminal recovery: %w", err)
	}
	return nil
}

func rejectSymlinkedParents(path string) error {
	dir := filepath.Dir(path)
	volume := filepath.VolumeName(dir)
	root := string(filepath.Separator)
	if volume != "" {
		root = volume + string(filepath.Separator)
	}
	relative, err := filepath.Rel(root, dir)
	if err != nil || relative == ".." || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return fmt.Errorf("terminal recovery path is unsafe")
	}
	current := root
	for _, component := range splitPath(relative) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect terminal recovery parent: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("terminal recovery path is unsafe")
		}
	}
	return nil
}

func splitPath(path string) []string {
	var components []string
	for path != "." && path != "" {
		dir, base := filepath.Split(path)
		if base != "" {
			components = append([]string{base}, components...)
		}
		path = filepath.Clean(dir)
		if path == string(filepath.Separator) {
			break
		}
	}
	return components
}

func validateRecovery(recovery TerminalRecovery) error {
	if !canonicalUUIDPattern.MatchString(recovery.SessionID) || !canonicalUUIDPattern.MatchString(recovery.CommandID) ||
		!canonicalUUIDPattern.MatchString(recovery.LeaseToken) {
		return fmt.Errorf("terminal recovery identifiers are invalid")
	}
	if !canonicalWholeSecondUTC(recovery.AgentConnectionDeadline) || !canonicalWholeSecondUTC(recovery.AbsoluteExpiresAt) ||
		!recovery.AbsoluteExpiresAt.After(recovery.AgentConnectionDeadline) {
		return fmt.Errorf("terminal recovery deadlines are invalid")
	}
	return nil
}

func canonicalWholeSecondUTC(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Nanosecond() == 0
}

func rejectDuplicateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("invalid key")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate key")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("invalid delimiter")
		}
	}
	return walk()
}
