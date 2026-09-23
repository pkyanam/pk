package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/extensions"
)

func TestInboxListUsesReadOnlyCLIAndBoundsLimit(t *testing.T) {
	var got []string
	w := worker{run: func(_ context.Context, args ...string) ([]byte, error) {
		got = args
		return []byte(`{"count":3,"inboxes":[{"inbox_id":"i1","email":"a@example.test","display_name":"A"},{"inbox_id":"i2"},{"inbox_id":"i3"}],"next_page_token":"` + strings.Repeat("x", maxID+1) + `"}`), nil
	}}
	result, err := w.ExecuteTool(context.Background(), extensions.ToolExecuteParams{Name: "mail_inboxes", Arguments: json.RawMessage(`{"limit":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"inboxes", "list", "--limit", "2"}) {
		t.Fatalf("unexpected command: %#v", got)
	}
	var decoded struct {
		Count   int               `json:"count"`
		Inboxes []json.RawMessage `json:"inboxes"`
		Next    string            `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Count != 2 || len(decoded.Inboxes) != 2 {
		t.Fatalf("provider limit was not enforced: %#v", decoded)
	}
	if decoded.Next != "" {
		t.Fatalf("oversized page token was returned (%d bytes)", len(decoded.Next))
	}
	if len(result.Content[0].Text) > 10_000 {
		t.Fatalf("inbox response was not bounded: %d bytes", len(result.Content[0].Text))
	}
	if strings.Contains(result.Content[0].Text, "metadata") {
		t.Fatal("inbox metadata should not be returned")
	}
}

func TestListAndGetUseOnlyReadOperations(t *testing.T) {
	calls := [][]string{}
	w := worker{run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[2] {
		case "list":
			return []byte(`{"count":1,"messages":[{"message_id":"m1","subject":"s","preview":"p","from":"sender","to":["r"]}]}`), nil
		case "get":
			return []byte(`{"inbox_id":"i1","message_id":"m1","text":"hello","html":"<b>secret html omitted</b>"}`), nil
		default:
			t.Fatalf("unexpected command %v", args)
			return nil, nil
		}
	}}
	if _, err := w.ExecuteTool(context.Background(), extensions.ToolExecuteParams{Name: "mail_list", Arguments: json.RawMessage(`{"inbox_id":"i1","page_token":"next"}`)}); err != nil {
		t.Fatal(err)
	}
	got, err := w.ExecuteTool(context.Background(), extensions.ToolExecuteParams{Name: "mail_get", Arguments: json.RawMessage(`{"inbox_id":"i1","message_id":"m1"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%d", len(calls))
	}
	if calls[0][1] != "messages" || calls[0][2] != "list" || calls[1][1] != "messages" || calls[1][2] != "get" {
		t.Fatalf("non-read commands: %#v", calls)
	}
	if !strings.Contains(got.Content[0].Text, "untrusted data") || strings.Contains(got.Content[0].Text, "secret html") {
		t.Fatalf("unsafe or excessive body result: %s", got.Content[0].Text)
	}
}

func TestRejectsInvalidArgumentsAndCapsMessageBody(t *testing.T) {
	w := worker{run: func(_ context.Context, args ...string) ([]byte, error) {
		body := strings.Repeat("x", maxText+100)
		data, _ := json.Marshal(map[string]string{"inbox_id": "i", "message_id": "m", "text": body})
		return data, nil
	}}
	if _, err := w.ExecuteTool(context.Background(), extensions.ToolExecuteParams{Name: "mail_get", Arguments: json.RawMessage(`{"inbox_id":""}`)}); err == nil {
		t.Fatal("expected required message_id/inbox validation")
	}
	result, err := w.ExecuteTool(context.Background(), extensions.ToolExecuteParams{Name: "mail_get", Arguments: json.RawMessage(`{"inbox_id":"i","message_id":"m"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content[0].Text) > maxText+1024 {
		t.Fatalf("response too large: %d", len(result.Content[0].Text))
	}
	if !strings.Contains(result.Content[0].Text, `"text_truncated":true`) {
		t.Fatalf("missing truncation marker: %s", result.Content[0].Text[len(result.Content[0].Text)-128:])
	}
}

func TestMessageListEnforcesProviderLimitAndTokenBound(t *testing.T) {
	w := worker{run: func(_ context.Context, args ...string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"inboxes", "messages", "list", "--inbox-id=inbox", "--limit", "1"}) {
			t.Fatalf("unexpected command: %#v", args)
		}
		return []byte(`{"count":3,"messages":[{"message_id":"m1"},{"message_id":"m2"},{"message_id":"m3"}],"next_page_token":"` + strings.Repeat("p", maxID+1) + `"}`), nil
	}}
	result, err := w.ExecuteTool(context.Background(), extensions.ToolExecuteParams{Name: "mail_list", Arguments: json.RawMessage(`{"inbox_id":"inbox","limit":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Count    int               `json:"count"`
		Messages []json.RawMessage `json:"messages"`
		Next     string            `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Count != 1 || len(decoded.Messages) != 1 || decoded.Next != "" {
		t.Fatalf("unbounded response: %#v", decoded)
	}
}

func TestRejectsExtraJSONArguments(t *testing.T) {
	w := newWorker()
	_, err := w.ExecuteTool(context.Background(), extensions.ToolExecuteParams{Name: "mail_inboxes", Arguments: json.RawMessage(`{"send_email":true}`)})
	if err == nil {
		t.Fatal("expected unknown argument to be rejected")
	}
}

func TestRejectsTrailingJSONValues(t *testing.T) {
	w := newWorker()
	_, err := w.ExecuteTool(context.Background(), extensions.ToolExecuteParams{Name: "mail_inboxes", Arguments: json.RawMessage(`{} {"limit":1}`)})
	if err == nil {
		t.Fatal("expected trailing JSON value to be rejected")
	}
}

func TestSafeCLIErrorSurfacesNotFoundWithoutProviderBody(t *testing.T) {
	raw := []byte(`{"error":{"code":404,"reason":"not_found","message":"Message not found","details":{"body":"private-message-body","id":"private-id"}}}`)
	got := safeCLIError(raw)
	if !strings.Contains(got, "not found") || !strings.Contains(got, "mail_list") {
		t.Fatalf("missing actionable not-found diagnostic: %q", got)
	}
	if strings.Contains(got, "private-message-body") || strings.Contains(got, "private-id") {
		t.Fatalf("provider details leaked: %q", got)
	}
	if got := safeCLIError([]byte(`{"error":{"code":500,"reason":"bad","message":"private body"}}`)); got != "" {
		t.Fatalf("unrecognized provider error should not be surfaced: %q", got)
	}
}
