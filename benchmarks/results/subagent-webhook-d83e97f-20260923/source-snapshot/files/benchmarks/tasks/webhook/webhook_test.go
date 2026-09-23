package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func signedRequest(secret []byte, body []byte) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	request.Header.Set(SignatureHeader, "sha256="+Sign(secret, body))
	return request
}

func TestSignatureUsesExactBodyAndRejectsTampering(t *testing.T) {
	secret := []byte("fixture-secret")
	body := []byte(`{"id":"evt-1","type":"build","payload":{"ok":true}}`)
	signature := Sign(secret, body)
	if !VerifySignature(secret, body, signature) || !VerifySignature(secret, body, "sha256="+signature) {
		t.Fatal("expected valid raw and prefixed HMAC signatures")
	}
	if VerifySignature(secret, append(body, ' '), signature) || VerifySignature([]byte("wrong"), body, signature) {
		t.Fatal("signature verification accepted a changed body or key")
	}
}

func TestEventRequiresIdentityAndObjectPayload(t *testing.T) {
	valid := Event{ID: " evt-1 ", Type: "build", Payload: json.RawMessage(`{"ok":true}`)}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	for _, event := range []Event{
		{Type: "build", Payload: json.RawMessage(`{}`)},
		{ID: "evt-1", Payload: json.RawMessage(`{}`)},
		{ID: "evt-1", Type: "build", Payload: json.RawMessage(`null`)},
		{ID: "evt-1", Type: "build", Payload: json.RawMessage(`[]`)},
		{ID: "evt-1", Type: "build", Payload: json.RawMessage(`{"x":`)},
	} {
		if err := event.Validate(); err == nil {
			t.Errorf("invalid event accepted: %+v", event)
		}
	}
}

func TestMemoryStoreDeduplicatesConcurrentlyAndCopiesPayload(t *testing.T) {
	store := NewMemoryStore()
	payload := json.RawMessage(`{"version":1}`)
	event := Event{ID: "evt-1", Type: "build", Payload: payload}
	const callers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	inserted := 0
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.InsertIfAbsent(context.Background(), event)
			if err != nil {
				t.Errorf("insert event: %v", err)
			}
			if ok {
				mu.Lock()
				inserted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if inserted != 1 {
		t.Fatalf("first insert count = %d, want 1", inserted)
	}
	payload[11] = '9'
	stored, ok := store.Get("evt-1")
	if !ok || string(stored.Payload) != `{"version":1}` {
		t.Fatalf("stored event changed with caller payload: %+v, found=%t", stored, ok)
	}
	stored.Payload[11] = '8'
	storedAgain, _ := store.Get("evt-1")
	if string(storedAgain.Payload) != `{"version":1}` {
		t.Fatal("Get returned mutable storage")
	}
	if _, err := store.InsertIfAbsent(context.Background(), Event{ID: "evt-2"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.InsertIfAbsent(ctx, Event{ID: "evt-3"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled insert error = %v", err)
	}
}

func TestHandlerAcceptsOnceAndReturnsDuplicate(t *testing.T) {
	secret := []byte("fixture-secret")
	store := NewMemoryStore()
	handler := NewHandler(secret, store)
	body := []byte(`{"id":"evt-1","type":"build","payload":{"status":"passed"}}`)
	for index, wantStatus := range []int{http.StatusAccepted, http.StatusOK} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedRequest(secret, body))
		if recorder.Code != wantStatus {
			t.Fatalf("request %d status = %d, want %d: %s", index, recorder.Code, wantStatus, recorder.Body.String())
		}
		if recorder.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("request %d content type = %q", index, recorder.Header().Get("Content-Type"))
		}
		var response struct {
			Accepted  bool `json:"accepted"`
			Duplicate bool `json:"duplicate"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("request %d response is not JSON: %v", index, err)
		}
		if !response.Accepted || response.Duplicate != (index == 1) {
			t.Fatalf("request %d response = %+v", index, response)
		}
	}
	if _, ok := store.Get("evt-1"); !ok {
		t.Fatal("accepted event was not persisted")
	}
}

type failingStore struct{}

func (failingStore) InsertIfAbsent(context.Context, Event) (bool, error) {
	return false, errors.New("private storage detail")
}

func TestHandlerHidesStoreErrorsAndReportsUnavailableConfiguration(t *testing.T) {
	secret := []byte("fixture-secret")
	body := []byte(`{"id":"evt-1","type":"build","payload":{}}`)
	for name, handler := range map[string]http.Handler{
		"store failure":  NewHandler(secret, failingStore{}),
		"missing store":  NewHandler(secret, nil),
		"missing secret": NewHandler(nil, NewMemoryStore()),
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := signedRequest(secret, body)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
			}
			if bytes.Contains(recorder.Body.Bytes(), []byte("private storage detail")) {
				t.Fatalf("response exposed internal error: %q", recorder.Body.String())
			}
		})
	}
}

func TestHandlerRejectsInvalidRequestsWithoutPersisting(t *testing.T) {
	secret := []byte("fixture-secret")
	store := NewMemoryStore()
	handler := NewHandler(secret, store)
	tests := []struct {
		name   string
		method string
		body   []byte
		signed bool
		want   int
	}{
		{name: "bad signature", method: http.MethodPost, body: []byte(`{}`), want: http.StatusUnauthorized},
		{name: "malformed JSON", method: http.MethodPost, body: []byte(`{`), signed: true, want: http.StatusBadRequest},
		{name: "trailing JSON", method: http.MethodPost, body: []byte(`{} {}`), signed: true, want: http.StatusBadRequest},
		{name: "unknown field", method: http.MethodPost, body: []byte(`{"id":"x","type":"build","payload":{},"admin":true}`), signed: true, want: http.StatusBadRequest},
		{name: "oversized", method: http.MethodPost, body: bytes.Repeat([]byte("x"), MaxBodyBytes+1), signed: true, want: http.StatusRequestEntityTooLarge},
		{name: "wrong method", method: http.MethodGet, body: []byte(`{}`), want: http.StatusMethodNotAllowed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/webhook", bytes.NewReader(test.body))
			if test.signed {
				request.Header.Set(SignatureHeader, Sign(secret, test.body))
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
	if _, ok := store.Get("x"); ok {
		t.Fatal("invalid request was persisted")
	}
}
