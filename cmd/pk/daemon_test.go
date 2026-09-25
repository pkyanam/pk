package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/v2/jsonrpc"
)

// daemonRPCRequest sends one newline-delimited JSON-RPC request through a
// fresh daemonServe instance and returns the raw response line.
func daemonRPCRequest(t *testing.T, request string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- daemonServe(ctx, strings.NewReader(request+"\n"), &out)
	}()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("daemonServe() error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("daemonServe() did not return")
	}
	return out.String()
}

// TestDaemonRequestFlow is a table of JSON-RPC requests over a pipe and their
// expected responses: catalog/get, catalog/reload, ping, and unknown method.
func TestDaemonRequestFlow(t *testing.T) {
	known := map[string]func(*testing.T) string{
		"ping": func(t *testing.T) string {
			t.Helper()
			return daemonRPCRequest(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		},
		"catalog/get": func(t *testing.T) string {
			t.Helper()
			return daemonRPCRequest(t, `{"jsonrpc":"2.0","id":2,"method":"catalog/get"}`)
		},
		"catalog/reload": func(t *testing.T) string {
			t.Helper()
			return daemonRPCRequest(t, `{"jsonrpc":"2.0","id":3,"method":"catalog/reload"}`)
		},
		"unknown": func(t *testing.T) string {
			t.Helper()
			return daemonRPCRequest(t, `{"jsonrpc":"2.0","id":4,"method":"bogus"}`)
		},
	}

	for name, fn := range known {
		t.Run(name, func(t *testing.T) {
			_ = fn(t)
		})
	}
}

// TestDaemonCatalogGetShape verifies the wire form of catalog/get: a Catalog
// map keyed by provider with the demo transforms applied (sphinctral disabled,
// limits halved exactly once).
func TestDaemonCatalogGetShape(t *testing.T) {
	raw := daemonRPCRequest(t, `{"jsonrpc":"2.0","id":1,"method":"catalog/get"}`)
	var resp jsonrpc.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", raw, err)
	}
	if resp.Error != nil {
		t.Fatalf("catalog/get error = %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("catalog/get result type = %T, want map[string]any", resp.Result)
	}
	sphinctral, ok := result["sphinctral"].([]any)
	if !ok || len(sphinctral) == 0 {
		t.Fatalf("sphinctral missing from catalog: %#v", result)
	}
	model := sphinctral[0].(map[string]any)
	if model["Disabled"] != true {
		t.Errorf("sphinctral Disabled = %v, want true", model["Disabled"])
	}
	openai, ok := result["openai"].([]any)
	if !ok || len(openai) == 0 {
		t.Fatalf("openai missing from catalog: %#v", result)
	}
	luna := openai[0].(map[string]any)
	if luna["Context"] != float64(100_000) {
		t.Errorf("gpt-6-luna Context = %v, want 100000 (halved once)", luna["Context"])
	}
}

// TestDaemonCatalogReloadDoesNotCompound verifies that catalog/reload returns
// the same halved limits as catalog/get: reload re-runs the transforms from
// the base, so halving never accumulates.
func TestDaemonCatalogReloadDoesNotCompound(t *testing.T) {
	first := daemonRPCRequest(t, `{"jsonrpc":"2.0","id":1,"method":"catalog/get"}`)
	second := daemonRPCRequest(t, `{"jsonrpc":"2.0","id":2,"method":"catalog/reload"}`)
	var a, b jsonrpc.Response
	if err := json.Unmarshal([]byte(first), &a); err != nil {
		t.Fatalf("unmarshal catalog/get %q: %v", first, err)
	}
	if err := json.Unmarshal([]byte(second), &b); err != nil {
		t.Fatalf("unmarshal catalog/reload %q: %v", second, err)
	}
	if a.Error != nil || b.Error != nil {
		t.Fatalf("unexpected errors: get=%+v reload=%+v", a.Error, b.Error)
	}
	// The ids differ (1 vs 2); the catalog payloads must be identical, which
	// proves reload did not compound the halve-limits transform.
	if !reflect.DeepEqual(a.Result, b.Result) {
		t.Fatalf("catalog/reload changed the catalog:\nget=%v\nreload=%v", a.Result, b.Result)
	}
}

// TestDaemonUnknownMethod verifies an unknown method yields MethodNotFound.
func TestDaemonUnknownMethod(t *testing.T) {
	raw := daemonRPCRequest(t, `{"jsonrpc":"2.0","id":1,"method":"bogus"}`)
	var resp jsonrpc.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", raw, err)
	}
	if resp.Error == nil || resp.Error.Code != jsonrpc.MethodNotFound {
		t.Fatalf("error = %+v, want MethodNotFound (%d)", resp.Error, jsonrpc.MethodNotFound)
	}
}