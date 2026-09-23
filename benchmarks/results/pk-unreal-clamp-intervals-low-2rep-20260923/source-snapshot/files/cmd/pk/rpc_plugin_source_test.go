package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkyanam/pk/internal/extensions"
)

func TestRPCPluginSourceDiscoverAndInstallIsExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	source := t.TempDir()
	component := filepath.Join(source, "sample")
	if err := os.MkdirAll(component, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := extensions.Manifest{
		APIVersion: extensions.ProtocolVersion, ID: "sample_plugin", Version: "1.0.0", Executable: "/bin/echo",
		Tools: []extensions.ToolSpec{{Name: "sample_tool", Description: "A local fixture tool", Parameters: json.RawMessage("{\"type\":\"object\",\"additionalProperties\":false}")}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(component, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, started: true, requestTypes: map[string]string{}}

	discoverRequest, _ := json.Marshal(map[string]string{"source": source})
	server.handle(rpcMessage{Version: 1, ID: "discover", Type: "plugins_discover", Payload: discoverRequest}, make(chan turnDone, 1))
	if started := readRPCEvent(t, sink); started.Type != "plugin_discover_started" {
		t.Fatalf("discover started event=%+v", started)
	}
	catalog := readRPCEvent(t, sink)
	if catalog.Type != "plugin_candidates" || catalog.ID != "discover" {
		t.Fatalf("catalog event=%+v", catalog)
	}
	var catalogPayload map[string]any
	encoded, _ := json.Marshal(catalog.Payload)
	if err := json.Unmarshal(encoded, &catalogPayload); err != nil {
		t.Fatal(err)
	}
	revision, _ := catalogPayload["revision"].(string)
	candidates, _ := catalogPayload["candidates"].([]any)
	if revision == "" || len(candidates) != 1 {
		t.Fatalf("catalog=%+v", catalogPayload)
	}
	candidate, _ := candidates[0].(map[string]any)
	if candidate["id"] != "sample_plugin" {
		t.Fatalf("candidate=%+v", candidate)
	}

	installRequest, _ := json.Marshal(map[string]string{"source": source, "manifest_path": candidate["manifest_path"].(string), "revision": revision})
	server.handle(rpcMessage{Version: 1, ID: "install", Type: "plugins_install", Payload: installRequest}, make(chan turnDone, 1))
	if started := readRPCEvent(t, sink); started.Type != "plugin_install_started" {
		t.Fatalf("install started event=%+v", started)
	}
	installed := readRPCEvent(t, sink)
	if installed.Type != "plugins_updated" || installed.ID != "install" {
		t.Fatalf("install event=%+v", installed)
	}
	plugins, err := userPluginService().List()
	if err != nil || len(plugins) != 1 || plugins[0].ID != "sample_plugin" {
		t.Fatalf("enabled plugin list=%+v err=%v", plugins, err)
	}

	removeRequest, _ := json.Marshal(map[string]string{"id": "sample_plugin"})
	server.handle(rpcMessage{Version: 1, ID: "remove", Type: "plugins_remove", Payload: removeRequest}, make(chan turnDone, 1))
	removed := readRPCEvent(t, sink)
	if removed.Type != "plugins_updated" || removed.ID != "remove" {
		t.Fatalf("remove event=%+v", removed)
	}
	if plugins, err := userPluginService().List(); err != nil || len(plugins) != 0 {
		t.Fatalf("plugin remains after remove: %+v err=%v", plugins, err)
	}
}
