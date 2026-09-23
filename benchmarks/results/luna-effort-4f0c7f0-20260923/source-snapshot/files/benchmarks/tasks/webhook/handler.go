package webhook

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

const (
	SignatureHeader = "X-PK-Signature"
	MaxBodyBytes    = 1 << 20
)

type Handler struct {
	secret []byte
	store  Store
}

func NewHandler(secret []byte, store Store) *Handler {
	return &Handler{secret: append([]byte(nil), secret...), store: store}
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if handler.store == nil || len(handler.secret) == 0 {
		http.Error(w, "webhook handler is not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, MaxBodyBytes+1))
	if err != nil {
		http.Error(w, "could not read request body", http.StatusBadRequest)
		return
	}
	if len(body) > MaxBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !VerifySignature(handler.secret, body, request.Header.Get(SignatureHeader)) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var event Event
	if err := decoder.Decode(&event); err != nil {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	if err := event.Validate(); err != nil {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	// TODO: Insert atomically. Return 202 for a new event, 200 for a duplicate,
	// and 503 when persistence fails. Keep all error responses free of internals.
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
