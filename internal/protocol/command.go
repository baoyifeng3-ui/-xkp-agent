package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"
)

const MaxCommandBytes = 4096

type CommandType string

const ShutdownServer CommandType = "SHUTDOWN_SERVER"

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type Command struct {
	CommandID      string          `json:"commandId"`
	Type           CommandType     `json:"type"`
	Version        int             `json:"version"`
	LeaseToken     string          `json:"leaseToken"`
	LeaseExpiresAt time.Time       `json:"leaseExpiresAt"`
	Payload        json.RawMessage `json:"payload"`
}

func DecodeCommand(data []byte) (Command, error) {
	if len(data) == 0 || len(data) > MaxCommandBytes {
		return Command{}, fmt.Errorf("command envelope size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var command Command
	if err := decoder.Decode(&command); err != nil {
		return Command{}, fmt.Errorf("decode command envelope: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return Command{}, err
	}
	if !uuidPattern.MatchString(command.CommandID) || !uuidPattern.MatchString(command.LeaseToken) {
		return Command{}, fmt.Errorf("command identifiers are invalid")
	}
	if command.Type != ShutdownServer || command.Version != 1 {
		return Command{}, fmt.Errorf("command type or version is unsupported")
	}
	if command.LeaseExpiresAt.IsZero() {
		return Command{}, fmt.Errorf("command lease expiry is required")
	}
	var payload map[string]json.RawMessage
	if len(command.Payload) == 0 || json.Unmarshal(command.Payload, &payload) != nil || payload == nil || len(payload) != 0 {
		return Command{}, fmt.Errorf("shutdown payload must be an empty object")
	}
	return command, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing interface{}
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("command envelope contains trailing JSON")
	}
	return fmt.Errorf("command envelope trailing data is invalid")
}
