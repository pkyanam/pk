package workspacejournal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// marshalLine encodes one journal line with a trailing newline.
func marshalLine(line preparedLine) ([]byte, error) {
	encoded, err := json.Marshal(line)
	if err != nil {
		return nil, fmt.Errorf("encode journal line: %w", err)
	}
	if bytes.ContainsRune(encoded, '\n') {
		return nil, errors.New("journal line encoding must not contain a newline")
	}
	return append(encoded, '\n'), nil
}

func unmarshalLine(data []byte, line *preparedLine) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(line); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("journal line contains trailing data")
	}
	return nil
}

// readRawLines reads every decodable journal line plus the byte length of the
// truncated tail, if any. A malformed non-tail line is a corruption error.
func readRawLines(path string) ([]preparedLine, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("read journal: %w", err)
	}
	var lines []preparedLine
	offset := 0
	for offset < len(data) {
		end := bytes.IndexByte(data[offset:], '\n')
		if end < 0 {
			break
		}
		var line preparedLine
		if err := unmarshalLine(data[offset:offset+end], &line); err != nil {
			return lines, len(data) - offset, err
		}
		lines = append(lines, line)
		offset += end + 1
	}
	return lines, len(data) - offset, nil
}

// errorMessage normalizes an error for storage in the journal. Reasons are
// bounded and flattened to one line so they never carry file content.
func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}
