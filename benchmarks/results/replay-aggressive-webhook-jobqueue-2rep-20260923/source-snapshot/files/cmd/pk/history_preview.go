package main

import (
	"strings"
	"unicode/utf8"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// assistantHistoryPreview returns the same tail that projectHistory previously
// produced by joining all assistant messages and then calling tailUTF8, while
// retaining no more than maxBytes of message text at a time.
func assistantHistoryPreview(outputs []llm.Item, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", false
	}
	buffer := make([]byte, 0, maxBytes)
	truncated := false
	appendText := func(text string) {
		if len(text) >= maxBytes {
			if len(text) > maxBytes || len(buffer) > 0 {
				truncated = true
			}
			buffer = append(buffer[:0], text[len(text)-maxBytes:]...)
			return
		}
		remove := len(buffer) + len(text) - maxBytes
		if remove > 0 {
			truncated = true
			copy(buffer, buffer[remove:])
			buffer = buffer[:len(buffer)-remove]
		}
		buffer = append(buffer, text...)
	}
	parts := 0
	for _, output := range outputs {
		if output.Type != llm.ItemMessage {
			continue
		}
		message, ok := output.Data.(llm.Message)
		if !ok || message.Role != llm.RoleAssistant || message.Phase == "analysis" || strings.TrimSpace(message.Text) == "" {
			continue
		}
		// Persisted model text is normally valid UTF-8 (it has passed through
		// JSON decoding). Keep the old normalization behavior for malformed
		// strings by falling back to the exact join-then-tail implementation.
		if !utf8.ValidString(message.Text) {
			return legacyAssistantHistoryPreview(outputs, maxBytes)
		}
		if parts > 0 {
			appendText("\n")
		}
		appendText(message.Text)
		parts++
	}
	start := 0
	for start < len(buffer) && !utf8.RuneStart(buffer[start]) {
		start++
	}
	return string(buffer[start:]), truncated
}

func legacyAssistantHistoryPreview(outputs []llm.Item, maxBytes int) (string, bool) {
	parts := make([]string, 0, len(outputs))
	for _, output := range outputs {
		if output.Type != llm.ItemMessage {
			continue
		}
		message, ok := output.Data.(llm.Message)
		if ok && message.Role == llm.RoleAssistant && message.Phase != "analysis" && strings.TrimSpace(message.Text) != "" {
			parts = append(parts, message.Text)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	joined := strings.Join(parts, "\n")
	if len(joined) > maxBytes {
		return tailUTF8(joined, maxBytes), true
	}
	return joined, false
}
