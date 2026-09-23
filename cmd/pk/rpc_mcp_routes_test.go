package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
)

func TestRPCMCPConfigurationRoutesDoNotLaunchServer(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, sessionDir: t.TempDir(), started: true, requestTypes: map[string]string{}}
	send := func(id, typ, payload, want string) rpcEvent {
		t.Helper()
		server.handle(rpcMessage{Version: 1, ID: id, Type: typ, Payload: json.RawMessage(payload)}, make(chan turnDone, 1))
		event := readRPCEvent(t, sink)
		if event.Type != want {
			t.Fatalf("%s response=%+v, want %s", typ, event, want)
		}
		return event
	}
	decode := func(event rpcEvent, out any) {
		t.Helper()
		data, err := json.Marshal(event.Payload)
		if err == nil {
			err = json.Unmarshal(data, out)
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	// The configured executable is intentionally absent. Listing and mutating
	// config must not start it; the server launches only for a model run.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	serverConfig, _ := json.Marshal(map[string]any{"server": map[string]any{"id": "not-running", "command": executable}})
	added := send("add", "mcp_add", string(serverConfig), "mcp_updated")
	var payload mcpCatalogPayload
	decode(added, &payload)
	if len(payload.Servers) != 1 || payload.Servers[0].ID != "not-running" || !payload.NextSessionOnly {
		t.Fatalf("add response=%+v", payload)
	}
	listed := send("list", "mcp_list", `{}`, "mcp_catalog")
	decode(listed, &payload)
	if len(payload.Servers) != 1 {
		t.Fatalf("list response=%+v", payload)
	}
	tools := send("tools", "tools", `{}`, "tool_catalog")
	var toolPayload savedToolCatalogPayload
	decode(tools, &toolPayload)
	if toolPayload.Saved {
		t.Fatalf("unexpected saved tool catalog: %+v", toolPayload)
	}
	removed := send("remove", "mcp_remove", `{"id":"not-running"}`, "mcp_updated")
	decode(removed, &payload)
	if len(payload.Servers) != 0 {
		t.Fatalf("remove response=%+v", payload)
	}
}
