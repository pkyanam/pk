package main

import (
	"encoding/json"
	"encoding/json/jsontext"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/presentation"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
)

func TestEnrichHistoryPresentationRequiresMatchingInputAndPayloadHashes(t *testing.T) {
	sessionDir := t.TempDir()
	const sessionID, inputID = "session-a", "input-a"
	userText := "Summarize this"
	effective := userText + "\n\n[generated file note with extracted data]"
	record, err := presentation.NewRecord(userText, effective, []attachments.Attachment{{Path: "reports/summary.txt", Kind: attachments.Text, ContentType: "text/plain"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := presentation.Save(sessionDir, sessionID, inputID, record); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(effective)
	items := []sessionstore.Item{{Sequence: 10, Kind: sessionstore.ItemInput, Data: inbox.Input{ID: inbox.ID(inputID), Kind: inbox.InputExternal, Payload: jsontext.Value(payload)}}}
	entries := []historyEntry{{Role: "user", Text: effective, Sequence: 10}}
	got, _, _ := enrichHistoryPresentation(sessionDir, sessionID, items, entries)
	if got[0].Text != userText || len(got[0].Attachments) != 1 || got[0].Attachments[0].Name != "summary.txt" {
		t.Fatalf("enriched entry=%+v", got[0])
	}
	// Wrong IDs and changed suffixes keep the full durable text visible.
	wrongID := []sessionstore.Item{{Sequence: 11, Kind: sessionstore.ItemInput, Data: inbox.Input{ID: "different", Kind: inbox.InputExternal, Payload: jsontext.Value(payload)}}}
	fallback, _, _ := enrichHistoryPresentation(sessionDir, sessionID, wrongID, []historyEntry{{Role: "user", Text: effective, Sequence: 11}})
	if fallback[0].Text != effective || len(fallback[0].Attachments) != 0 {
		t.Fatalf("wrong ID changed legacy prompt: %+v", fallback[0])
	}
	changed := userText + "\n\n[different suffix]"
	changedPayload, _ := json.Marshal(changed)
	bad := []sessionstore.Item{{Sequence: 12, Kind: sessionstore.ItemInput, Data: inbox.Input{ID: inbox.ID(inputID), Kind: inbox.InputExternal, Payload: jsontext.Value(changedPayload)}}}
	fallback, _, _ = enrichHistoryPresentation(sessionDir, sessionID, bad, []historyEntry{{Role: "user", Text: changed, Sequence: 12}})
	if fallback[0].Text != changed || len(fallback[0].Attachments) != 0 {
		t.Fatalf("payload mismatch changed legacy prompt: %+v", fallback[0])
	}
}

func TestEnrichHistoryPresentationKeepsHistoryBoundsForLargePromptAndChips(t *testing.T) {
	sessionDir := t.TempDir()
	const sessionID, inputID = "large-session", "large-input"
	userText := strings.Repeat("é", 12_000)
	effective := userText + "\n\n[attachment note]"
	itemsForRecord := make([]attachments.Attachment, presentation.MaxAttachments)
	for i := range itemsForRecord {
		itemsForRecord[i] = attachments.Attachment{Path: strings.Repeat("n", 240) + string(rune('a'+i)) + ".txt", Kind: attachments.Text, ContentType: "text/plain; charset=utf-8"}
	}
	record, err := presentation.NewRecord(userText, effective, itemsForRecord)
	if err != nil {
		t.Fatal(err)
	}
	if err := presentation.Save(sessionDir, sessionID, inputID, record); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(effective)
	items := []sessionstore.Item{{Sequence: 1, Kind: sessionstore.ItemInput, Data: inbox.Input{ID: inbox.ID(inputID), Kind: inbox.InputExternal, Payload: jsontext.Value(payload)}}}
	base, _, _ := projectHistory(items)
	got, earlier, truncated := enrichHistoryPresentation(sessionDir, sessionID, items, base)
	if earlier || !truncated || len(got) != 1 {
		t.Fatalf("bounded projection earlier=%v truncated=%v entries=%d", earlier, truncated, len(got))
	}
	if len(got[0].Text) > maxHistoryEntryBytes || historyEntrySize(got[0]) > maxHistoryEntryBytes || historyEntrySize(got[0]) > maxHistoryBytes {
		t.Fatalf("presentation exceeded history bounds: text=%d total=%d", len(got[0].Text), historyEntrySize(got[0]))
	}
	if !strings.HasPrefix(userText, strings.TrimSuffix(got[0].Text, "…")) || len(got[0].Attachments) != presentation.MaxAttachments {
		t.Fatalf("lost typed prefix or chips: %+v", got[0])
	}
}
