package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/extensions"
)

func TestPluginSlashCommandCatalogIsNamespacedWithoutStartingWorkers(t *testing.T) {
	dir := t.TempDir()
	first := writeCommandManifest(t, dir, "alpha", "true", "status", "Alpha status")
	second := writeCommandManifest(t, dir, "beta", "true", "status", "Beta status")
	commands, issues := pluginSlashCommandCatalog([]string{second, first})
	if len(issues) != 0 {
		t.Fatalf("catalog issues: %v", issues)
	}
	want := []extensions.SlashCommand{
		{Name: "/ext:alpha:status", ExtensionID: "alpha", CommandName: "status", Description: "Alpha status"},
		{Name: "/ext:beta:status", ExtensionID: "beta", CommandName: "status", Description: "Beta status"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("catalog=%+v want=%+v", commands, want)
	}
}

func TestPluginSlashCommandCatalogReportsInvalidExplicitManifest(t *testing.T) {
	commands, issues := pluginSlashCommandCatalog([]string{filepath.Join(t.TempDir(), "missing.json")})
	if len(commands) != 0 || len(issues) != 1 {
		t.Fatalf("commands=%v issues=%v", commands, issues)
	}
}

func TestExecutePluginSlashCommandRejectsOversizedArgumentsBeforeLoading(t *testing.T) {
	_, err := executePluginSlashCommand(t.Context(), t.TempDir(), nil, "/ext:alpha:status", strings.Repeat("x", extensions.MaxSlashCommandArgumentBytes+1))
	if err == nil || !strings.Contains(err.Error(), "arguments exceed") {
		t.Fatalf("oversized argument error=%v", err)
	}
}

func writeCommandManifest(t *testing.T, dir, id, executable, commandName, description string) string {
	t.Helper()
	manifest := extensions.Manifest{
		APIVersion: extensions.ProtocolVersion,
		ID:         id,
		Version:    "1.0.0",
		Executable: executable,
		Commands:   []extensions.CommandSpec{{Name: commandName, Description: description}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
