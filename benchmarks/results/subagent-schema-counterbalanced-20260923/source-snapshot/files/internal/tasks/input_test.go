package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPumpInputsPersistsCursorOnlyAfterAck(t *testing.T) {
	dir := t.TempDir()
	envelope, err := json.Marshal(inputEnvelope{ID: "input-1", Text: "follow-up"})
	if err != nil {
		t.Fatal(err)
	}
	line := append(envelope, '\n')
	if err := os.WriteFile(filepath.Join(dir, "inputs.jsonl"), line, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	inputs := make(chan Input)
	done := make(chan error, 1)
	go func() { done <- pumpInputs(ctx, dir, inputs) }()
	select {
	case input := <-inputs:
		if input.ID != "input-1" || input.Text != "follow-up" {
			t.Fatalf("unexpected input: %#v", input)
		}
		if _, err := os.Stat(filepath.Join(dir, "input.offset")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cursor persisted before acceptance: %v", err)
		}
		input.Ack(nil)
	case <-time.After(time.Second):
		t.Fatal("input was not delivered")
	}
	deadline := time.After(time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(dir, "input.offset"))
		if err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(len(line)) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("cursor not persisted after ack: %q, %v", b, err)
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("input pump did not stop after cancellation")
	}
}

func TestPumpInputsFailsOnUnrecoverableOffsetOrCorruptRecord(t *testing.T) {
	t.Run("offset persistence", func(t *testing.T) {
		dir := t.TempDir()
		line, _ := json.Marshal(inputEnvelope{ID: "input-1", Text: "follow-up"})
		if err := os.WriteFile(filepath.Join(dir, "inputs.jsonl"), append(line, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		inputs := make(chan Input)
		done := make(chan error, 1)
		go func() { done <- pumpInputs(ctx, dir, inputs) }()
		select {
		case input := <-inputs:
			// A directory at the offset path makes the atomic file replacement fail,
			// but is created after the pump's initial offset read.
			if err := os.Mkdir(filepath.Join(dir, "input.offset"), 0o700); err != nil {
				t.Fatal(err)
			}
			input.Ack(nil)
		case <-time.After(time.Second):
			t.Fatal("input was not delivered")
		}
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "persist accepted task input") {
				t.Fatalf("pump error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("offset failure did not stop the pump")
		}
	})

	t.Run("complete corrupt row", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "inputs.jsonl"), []byte("not json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := pumpInputs(context.Background(), dir, make(chan Input))
		if err == nil || !strings.Contains(err.Error(), "corrupt task input queue record at byte 0") {
			t.Fatalf("pump error = %v", err)
		}
	})
}
