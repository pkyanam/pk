package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type progressServeHandler struct{ announce bool }

func (h progressServeHandler) Initialize(_ context.Context, params InitializeParams) (InitializeResult, error) {
	for _, f := range params.HostFeatures {
		if f == HostFeatureToolProgress {
			h.announce = true
		}
	}
	return InitializeResult{APIVersion: ProtocolVersion, ID: params.ID}, nil
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
