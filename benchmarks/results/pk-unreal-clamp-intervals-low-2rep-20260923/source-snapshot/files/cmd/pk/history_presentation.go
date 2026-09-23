package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/pkyanam/pk/internal/presentation"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
)

func newPresentationPromptID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "pk_" + hex.EncodeToString(raw[:]), nil
}

// enrichHistoryPresentation replaces the generated attachment suffix only
// when a per-input provenance record verifies both the prompt prefix and full
// effective payload. Missing or invalid records leave legacy text untouched.
func enrichHistoryPresentation(sessionsDir, sessionID string, items []sessionstore.Item, entries []historyEntry) ([]historyEntry, bool, bool) {
	bySequence := make(map[uint64]int, len(entries))
	for index, entry := range entries {
		if entry.Role == "user" {
			bySequence[entry.Sequence] = index
		}
	}
	for _, item := range items {
		if item.Kind != sessionstore.ItemInput {
			continue
		}
		input, ok := item.Data.(inbox.Input)
		if !ok || input.Kind != inbox.InputExternal {
			continue
		}
		index, ok := bySequence[uint64(item.Sequence)]
		if !ok {
			continue
		}
		var fullPrompt string
		if json.Unmarshal([]byte(input.Payload), &fullPrompt) != nil {
			continue
		}
		record, err := presentation.Load(sessionsDir, sessionID, string(input.ID))
		if err != nil || !record.Matches(fullPrompt) {
			continue
		}
		entries[index].Text = fullPrompt[:record.PrefixBytes]
		entries[index].Attachments = append([]presentation.Attachment(nil), record.Attachments...)
	}
	return boundPresentedHistory(entries)
}

func boundPresentedHistory(entries []historyEntry) ([]historyEntry, bool, bool) {
	hasEarlier, truncated := false, false
	bounded := make([]historyEntry, 0, min(len(entries), maxHistoryEntries))
	readBytes := 0
	for _, entry := range entries {
		metadataBytes := historyEntrySize(entry) - len(entry.Text)
		entryTextLimit := maxHistoryEntryBytes - metadataBytes
		if entryTextLimit < 0 {
			entryTextLimit = 0
		}
		if maxHistoryBytes-metadataBytes < entryTextLimit {
			entryTextLimit = maxHistoryBytes - metadataBytes
		}
		if len(entry.Text) > entryTextLimit {
			entry.Text = historyPrefixUTF8(entry.Text, entryTextLimit)
			truncated = true
		}
		for len(bounded) > 0 && (len(bounded) >= maxHistoryEntries || readBytes+historyEntrySize(entry) > maxHistoryBytes) {
			// This is defensive for aggregate metadata added after base projection;
			// remove oldest visible rows so a subsequent cursor can request them.
			removed := historyEntrySize(bounded[0])
			bounded = bounded[1:]
			readBytes -= removed
			hasEarlier, truncated = true, true
		}
		bounded = append(bounded, entry)
		readBytes += historyEntrySize(entry)
	}
	return bounded, hasEarlier, truncated
}

func saveHistoryPresentation(sessionsDir, sessionID, inputID string, record presentation.Record, diagnostics io.Writer) {
	if err := presentation.Save(sessionsDir, sessionID, inputID, record); err != nil {
		if diagnostics != nil {
			_, _ = fmt.Fprintf(diagnostics, "pk: could not save attachment history metadata; full prompt will be retained: %v\n", err)
		}
	}
}
