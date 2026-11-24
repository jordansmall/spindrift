package driverkit

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// EncodeSessionArgs renders argv words as the session file's body: a JSON
// array, not a joined string, so a word with spaces stays one word through
// DecodeSessionArgs. No words render an empty file.
func EncodeSessionArgs(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", nil
	}
	b, err := json.Marshal(argv)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeSessionArgs reads the session file the Box writes: a JSON array of
// argv words, or an empty file for a session with no flag.
func DecodeSessionArgs(b []byte) ([]string, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal(b, &args); err != nil {
		return nil, fmt.Errorf("session file is not a JSON array of strings: %w", err)
	}
	return args, nil
}
