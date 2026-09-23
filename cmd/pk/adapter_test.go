package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/auth"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/responsesapi"
)

type adapterReply struct {
	response llm.Response
	err      error
}

type mockModelAdapter struct {
	mu         sync.Mutex
	replies    []adapterReply
	calls      int
	requests   []llm.Request
	closed     bool
	started    chan struct{}
	continueCh chan struct{}
}

func (adapter *mockModelAdapter) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	adapter.mu.Lock()
	adapter.calls++
	adapter.requests = append(adapter.requests, request)
	adapter.mu.Unlock()
	if adapter.started != nil {
		select {
		case adapter.started <- struct{}{}:
		default:
		}
	}
	if adapter.continueCh != nil {
		select {
		case <-adapter.continueCh:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
	if len(adapter.replies) == 0 {
		return llm.Response{}, errors.New("no scripted reply")
	}
	reply := adapter.replies[0]
	adapter.replies = adapter.replies[1:]
	return reply.response, reply.err
}

func (adapter *mockModelAdapter) Close() error {
	adapter.mu.Lock()
	adapter.closed = true
	adapter.mu.Unlock()
	return nil
}

func TestCodexAdapterReloadsCredentialsBeforeModelRequest(t *testing.T) {
	initial := auth.Credential{AccessToken: "old", AccountID: "account"}
	fresh := auth.Credential{AccessToken: "new", AccountID: "account"}
	oldClient := &mockModelAdapter{replies: []adapterReply{{err: errors.New("old client must not be used")}}}
	freshClient := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "fresh"}}}}
	clients := []*mockModelAdapter{oldClient, freshClient}
	adapter := &codexAdapter{
		credential: initial, client: oldClient, semaphore: make(chan struct{}, 1),
		get: func(context.Context) (auth.Credential, error) { return fresh, nil },
		refresh: func(context.Context) (auth.Credential, error) {
			t.Fatal("unexpected forced refresh")
			return auth.Credential{}, nil
		},
		newClient: func(got auth.Credential) (llm.Adapter, error) {
			if got != fresh {
				t.Fatalf("new client credential = %#v", got)
			}
			return clients[1], nil
		},
	}
	response, err := adapter.Respond(t.Context(), llm.Request{}, llm.RequestOptions{})
	if err != nil || response.ID != "fresh" {
		t.Fatalf("Respond() = (%#v, %v)", response, err)
	}
	if oldClient.calls != 0 || freshClient.calls != 1 || !oldClient.closed {
		t.Fatalf("old/new client state = %d/%d calls, closed=%v", oldClient.calls, freshClient.calls, oldClient.closed)
	}
}

func TestCodexAdapterRefreshesOnceOnUnauthorized(t *testing.T) {
	initial := auth.Credential{AccessToken: "old", AccountID: "account"}
	fresh := auth.Credential{AccessToken: "new", AccountID: "account"}
	oldClient := &mockModelAdapter{replies: []adapterReply{{err: &responsesapi.APIError{StatusCode: 401, Message: "expired"}}}}
	freshClient := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "retried"}}}}
	adapter := &codexAdapter{
		credential: initial, client: oldClient, semaphore: make(chan struct{}, 1),
		get:     func(context.Context) (auth.Credential, error) { return initial, nil },
		refresh: func(context.Context) (auth.Credential, error) { return fresh, nil },
		newClient: func(got auth.Credential) (llm.Adapter, error) {
			if got != fresh {
				t.Fatalf("refreshed credential = %#v", got)
			}
			return freshClient, nil
		},
	}
	response, err := adapter.Respond(t.Context(), llm.Request{}, llm.RequestOptions{})
	if err != nil || response.ID != "retried" {
		t.Fatalf("Respond() = (%#v, %v)", response, err)
	}
	if oldClient.calls != 1 || freshClient.calls != 1 || !oldClient.closed {
		t.Fatalf("old/new client state = %d/%d calls, closed=%v", oldClient.calls, freshClient.calls, oldClient.closed)
	}
}

func TestCodexImportNeverRefreshesOnUnauthorized(t *testing.T) {
	client := &mockModelAdapter{replies: []adapterReply{{err: &responsesapi.APIError{StatusCode: 401}}}}
	adapter := &codexAdapter{credential: auth.Credential{AccessToken: "old", AccountID: "account"}, client: client, useCodex: true, semaphore: make(chan struct{}, 1)}
	_, err := adapter.Respond(t.Context(), llm.Request{}, llm.RequestOptions{})
	if !isUnauthorized(err) || client.calls != 1 {
		t.Fatalf("Codex imported credential should fail once read-only: calls=%d err=%v", client.calls, err)
	}
}

func TestCodexAdapterWaitIsCancellationAwareAndDoesNotCloseInUseClient(t *testing.T) {
	client := &mockModelAdapter{started: make(chan struct{}, 1), continueCh: make(chan struct{})}
	adapter := &codexAdapter{credential: auth.Credential{AccessToken: "old", AccountID: "account"}, client: client, useCodex: true, semaphore: make(chan struct{}, 1)}
	firstDone := make(chan error, 1)
	go func() {
		_, err := adapter.Respond(context.Background(), llm.Request{}, llm.RequestOptions{})
		firstDone <- err
	}()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("first request did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := adapter.Respond(ctx, llm.Request{}, llm.RequestOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting Respond() error = %v", err)
	}
	if client.closed {
		t.Fatal("client was closed while its request was still active")
	}
	close(client.continueCh)
	if err := <-firstDone; err == nil {
		t.Fatal("first mock request should report no scripted response")
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if !client.closed {
		t.Fatal("Close() did not close the idle client")
	}
}

func TestCodexAdapterUsesAlreadyRotatedPkCredentialAfterUnauthorized(t *testing.T) {
	old := auth.Credential{AccessToken: "stale", AccountID: "account"}
	fresh := auth.Credential{AccessToken: "rotated-by-another-run", AccountID: "account"}
	oldClient := &mockModelAdapter{replies: []adapterReply{{err: &responsesapi.APIError{StatusCode: 401}}}}
	newClient := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "recovered"}}}}
	getCalls := 0
	adapter := &codexAdapter{
		credential: old, client: oldClient, semaphore: make(chan struct{}, 1),
		get: func(context.Context) (auth.Credential, error) {
			getCalls++
			if getCalls == 1 {
				return old, nil
			}
			return fresh, nil
		},
		refresh: func(context.Context) (auth.Credential, error) {
			t.Fatal("should reuse token another process already refreshed")
			return auth.Credential{}, nil
		},
		newClient: func(auth.Credential) (llm.Adapter, error) { return newClient, nil },
	}
	response, err := adapter.Respond(t.Context(), llm.Request{}, llm.RequestOptions{})
	if err != nil || response.ID != "recovered" {
		t.Fatalf("Respond() = (%#v, %v)", response, err)
	}
	if oldClient.calls != 1 || newClient.calls != 1 || !oldClient.closed {
		t.Fatalf("old/new client state = %d/%d calls, closed=%v", oldClient.calls, newClient.calls, oldClient.closed)
	}
}
