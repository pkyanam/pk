package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type lifecycleServeHandler struct{ received chan LifecycleEvent }

func (h lifecycleServeHandler) Initialize(_ context.Context, params InitializeParams) (InitializeResult, error) {
	result := InitializeResult{APIVersion: ProtocolVersion, ID: params.ID}
	for _, feature := range params.HostFeatures {
		if feature == HostFeatureLifecycle {
			result.Features = []string{HostFeatureLifecycle}
		}
	}
	return result, nil
}
func (h lifecycleServeHandler) ExecuteTool(context.Context, ToolExecuteParams) (ToolResult, error) {
	return ToolResult{}, errors.New("unused")
}
func (h lifecycleServeHandler) ExecuteCommand(context.Context, CommandExecuteParams) (string, error) {
	return "", errors.New("unused")
}
func (h lifecycleServeHandler) NotifyLifecycle(_ context.Context, event LifecycleEvent) error {
	h.received <- event
	return nil
}

func TestServeLifecycleRequiresNegotiatedWorkerOptIn(t *testing.T) {
	event := LifecycleEvent{Type: LifecycleRunStart, SessionID: "session-1", Model: "gpt-6-luna", Workspace: "/workspace", Status: "started"}
	encoded, _ := json.Marshal(event)
	request := `{"id":"init","method":"initialize","params":{"api_version":"pk.extensions/v1","id":"observer","host_features":["lifecycle_notifications"]}}` + "\n" +
		`{"id":"event","method":"lifecycle.notify","params":` + string(encoded) + "}\n"
	var output bytes.Buffer
	received := make(chan LifecycleEvent, 1)
	if err := Serve(context.Background(), strings.NewReader(request), &output, lifecycleServeHandler{received: received}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got != event {
			t.Fatalf("event=%+v want %+v", got, event)
		}
	default:
		t.Fatal("negotiated lifecycle event not delivered")
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || strings.Contains(lines[1], `"error"`) {
		t.Fatalf("lifecycle response: %s", output.String())
	}

	legacy := `{"id":"init","method":"initialize","params":{"api_version":"pk.extensions/v1","id":"observer"}}` + "\n" +
		`{"id":"event","method":"lifecycle.notify","params":` + string(encoded) + "}\n"
	output.Reset()
	if err := Serve(context.Background(), strings.NewReader(legacy), &output, lifecycleServeHandler{received: received}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "lifecycle notifications were not negotiated") {
		t.Fatalf("legacy worker unexpectedly accepted lifecycle event: %s", output.String())
	}
}

type progressServeHandler struct {
	announce  bool
	lifecycle bool
}

func (h progressServeHandler) Initialize(_ context.Context, params InitializeParams) (InitializeResult, error) {
	result := InitializeResult{APIVersion: ProtocolVersion, ID: params.ID}
	for _, f := range params.HostFeatures {
		if f == HostFeatureToolProgress {
			h.announce = true
		}
		if h.lifecycle && f == HostFeatureLifecycle {
			result.Features = append(result.Features, HostFeatureLifecycle)
		}
	}
	return result, nil
}

func TestServeRejectsLifecycleFeatureWithoutObserverImplementation(t *testing.T) {
	input := `{"id":"init","method":"initialize","params":{"api_version":"pk.extensions/v1","id":"sample","host_features":["lifecycle_notifications"]}}` + "\n"
	var output bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(input), &output, progressServeHandler{lifecycle: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "without implementing the observer") {
		t.Fatalf("invalid lifecycle feature claim was not rejected: %s", output.String())
	}
}
func (h progressServeHandler) ExecuteTool(ctx context.Context, _ ToolExecuteParams) (ToolResult, error) {
	for i := 0; i < MaxProgressEventsPerCall+3; i++ {
		ReportProgress(ctx, "step")
	}
	ReportProgress(ctx, "Bearer abc123\x1b[31m red\x1b[0m\x00")
	return ToolResult{Content: []Content{{Type: "text", Text: "done"}}}, nil
}
func (h progressServeHandler) ExecuteCommand(context.Context, CommandExecuteParams) (string, error) {
	return "ok", nil
}

func TestServeNegotiatesProgressBeforeFinalAndBoundsSanitizes(t *testing.T) {
	input := strings.Join([]string{
		`{"id":"init","method":"initialize","params":{"api_version":"v1","id":"sample","host_features":["tool_progress"]}}`,
		`{"id":"req-1","method":"tool.execute","params":{"name":"sample_tool","call_id":"model-call-9"}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(input), &output, progressServeHandler{}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	var progress int
	toolSeen := false
	for i, line := range lines {
		var frame map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("line %d invalid JSON: %v", i, err)
		}
		var method string
		_ = json.Unmarshal(frame["method"], &method)
		if method == "tool.progress" {
			if !toolSeen {
				t.Fatalf("progress before tool execution at line %d", i)
			}
			progress++
			var id string
			_ = json.Unmarshal(frame["id"], &id)
			if id != "req-1" {
				t.Fatalf("progress transport ID=%q", id)
			}
			var params struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(frame["params"], &params)
			if strings.Contains(params.Text, "abc123") || strings.ContainsRune(params.Text, '\x1b') || strings.ContainsRune(params.Text, '\x00') {
				t.Fatalf("unsanitized text %q", params.Text)
			}
			if len(params.Text) > MaxProgressTextBytes {
				t.Fatalf("oversized progress: %d", len(params.Text))
			}
			continue
		}
		var requestID string
		_ = json.Unmarshal(frame["id"], &requestID)
		if requestID == "init" {
			toolSeen = true
			continue
		}
		if i != len(lines)-1 {
			t.Fatalf("non-progress response before final at line %d", i)
		}
		if requestID != "req-1" {
			t.Fatalf("final response ID=%q", requestID)
		}
	}
	if progress != MaxProgressEventsPerCall {
		t.Fatalf("progress count=%d want %d", progress, MaxProgressEventsPerCall)
	}
}

func TestServeOldHostGetsFinalOnly(t *testing.T) {
	input := `{"id":"init","method":"initialize","params":{"api_version":"v1","id":"sample"}}` + "\n" +
		`{"id":"req-1","method":"tool.execute","params":{"name":"sample_tool","call_id":"model-call-9"}}` + "\n"
	var output bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(input), &output, progressServeHandler{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), `"method":"tool.progress"`) {
		t.Fatalf("unexpected progress for old host: %s", output.String())
	}
}

func TestProgressSanitizerBoundsUTF8(t *testing.T) {
	got := sanitizeProgress(strings.Repeat("é", MaxProgressTextBytes))
	if len(got) > MaxProgressTextBytes || !json.Valid([]byte(`"`+got+`"`)) {
		t.Fatalf("invalid bounded UTF-8: bytes=%d", len(got))
	}
	for _, secret := range []string{"abc123", "pw-123", "xyz", "quoted-access", "quoted-password"} {
		text := sanitizeProgress("Authorization: Bearer abc123 api_key=xyz password: pw-123\u0085 {\"api_key\":\"quoted-access\",\"password\":\"quoted-password\"}")
		if strings.Contains(text, secret) || strings.ContainsRune(text, '\u0085') {
			t.Fatalf("secret/control survived: %q", text)
		}
	}
}
