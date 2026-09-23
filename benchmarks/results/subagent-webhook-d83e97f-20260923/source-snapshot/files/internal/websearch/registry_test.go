package websearch

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/operation"
)

type fixtureSearchFetchClient struct {
	search []SearchResult
	fetch  FetchResponse
	err    error
}

func (c fixtureSearchFetchClient) Search(context.Context, string) ([]SearchResult, error) {
	return c.search, c.err
}
func (c fixtureSearchFetchClient) Fetch(context.Context, []string) (FetchResponse, error) {
	return c.fetch, c.err
}

func TestSuccessfulSearchAndFetchEnvelopesHaveUTCRequestRetrievalTime(t *testing.T) {
	client := fixtureSearchFetchClient{
		search: []SearchResult{{Title: "fixture", Date: "2024-01-02", URL: "https://example.test"}},
		fetch:  FetchResponse{Results: []FetchResult{{URL: "https://example.test", Text: "page"}}, Notice: "fixture notice"},
	}
	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{"WebSearch", `{"query":"price"}`, `"results":[{"position":0,"site_name":"","title":"fixture","snippet":"","url":"https://example.test","date":"2024-01-02"}]`},
		{"WebFetch", `{"urls":["https://example.test"]}`, `"text":"page"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now().UTC()
			state := runWebFixtureOperation(t, client, tc.name, tc.args)
			after := time.Now().UTC()
			if state.TerminalError != "" {
				t.Fatalf("terminal error=%q", state.TerminalError)
			}
			var envelope map[string]any
			if err := json.Unmarshal([]byte(state.TerminalResult), &envelope); err != nil {
				t.Fatalf("result=%q: %v", state.TerminalResult, err)
			}
			if !strings.Contains(state.TerminalResult, tc.want) {
				t.Fatalf("result omitted fixture data %q: %s", tc.want, state.TerminalResult)
			}
			stamp, ok := envelope["retrieved_at"].(string)
			if !ok || stamp == "" {
				t.Fatalf("retrieved_at missing: %s", state.TerminalResult)
			}
			retrieved, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil || retrieved.Location() != time.UTC || retrieved.Before(before.Truncate(time.Second)) || retrieved.After(after.Truncate(time.Second).Add(time.Second)) {
				t.Fatalf("retrieved_at=%q parsed=%v error=%v, want UTC timestamp within request", stamp, retrieved, err)
			}
			if tc.name == "WebSearch" && envelope["retrieved_at"] == "2024-01-02" {
				t.Fatal("retrieval time must not be confused with source date")
			}
		})
	}
}

func TestFailedSearchOrFetchHasNoRetrievalTimestamp(t *testing.T) {
	client := fixtureSearchFetchClient{err: errors.New("fixture request failed")}
	for _, tc := range []struct{ name, args string }{
		{"WebSearch", `{"query":"price"}`},
		{"WebFetch", `{"urls":["https://example.test"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := runWebFixtureOperation(t, client, tc.name, tc.args)
			if state.TerminalError == "" {
				t.Fatal("expected failed operation")
			}
			if state.TerminalResult != "" {
				t.Fatalf("failed operation returned a success result: %q", state.TerminalResult)
			}
		})
	}
}

func TestPartialFetchPreservesPerURLErrorsAndAddsRequestRetrievalTime(t *testing.T) {
	client := fixtureSearchFetchClient{fetch: FetchResponse{
		Results: []FetchResult{{URL: "https://ok.test", Text: "page"}},
		Errors:  []FetchError{{URL: "https://failed.test", Error: "fetch failed"}},
		Notice:  "partial fixture",
	}}
	state := runWebFixtureOperation(t, client, "WebFetch", `{"urls":["https://ok.test","https://failed.test"]}`)
	if state.TerminalError != "" {
		t.Fatalf("partial fetch became a terminal error: %q", state.TerminalError)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(state.TerminalResult), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["retrieved_at"] == nil || len(envelope["errors"].([]any)) != 1 || len(envelope["results"].([]any)) != 1 {
		t.Fatalf("partial fetch envelope=%v", envelope)
	}
}

func TestToolDescriptionsDistinguishRetrievalTimeFromSourceFreshness(t *testing.T) {
	for _, definition := range toolDefinitions() {
		if !strings.Contains(definition.Description, "`retrieved_at` is UTC retrieval time, not source freshness") {
			t.Fatalf("%s description omits freshness distinction: %q", definition.Name, definition.Description)
		}
	}
}

func runWebFixtureOperation(t *testing.T, client SearchFetchClient, name, args string) operation.RemoteJobState {
	t.Helper()
	ctx := context.Background()
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{
		Type: planType, Version: planVersion,
		Data: jsontext.Value(mustJSON(t, plan{Name: name, Args: json.RawMessage(args)})),
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := operation.Operation{ID: "fixture-op", Type: spec.Type, Version: spec.Version, Status: operation.StatusReady, MaxOutputLength: spec.MaxOutputLength, State: spec.State}
	state, err := operation.DecodeRemoteJobState(initial)
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := operation.UpdateRemoteJob(initial, state, operation.StatusAwaiting)
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{ctx: ctx, client: client, jobs: map[operation.ID]*job{}, updates: make(chan operation.Operation, 2)}
	j := &job{op: *awaiting.Operation}
	h.jobs[initial.ID] = j
	h.wg.Add(1)
	go h.execute(ctx, initial.ID, j, plan{Name: name, Args: json.RawMessage(args)})
	select {
	case completed := <-h.updates:
		state, err := operation.DecodeRemoteJobState(completed)
		if err != nil {
			t.Fatal(err)
		}
		return state
	case <-time.After(2 * time.Second):
		t.Fatal("fixture operation did not complete")
		return operation.RemoteJobState{}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
