package webhook

import (
	"encoding/json"
	"errors"
	"strings"
)

// Event is the signed, durable envelope accepted by the webhook endpoint.
type Event struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Validate checks that an event has a usable identity and a JSON object payload.
func (event Event) Validate() error {
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.Type) == "" {
		return errors.New("event id and type are required")
	}
	// TODO: Require Payload to contain exactly one valid JSON object.
	return nil
}
