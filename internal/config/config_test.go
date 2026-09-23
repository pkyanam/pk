package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "gpt-6-luna" || got.Effort != "medium" || got.ContextPolicy != ContextPolicyFull {
		t.Fatalf("defaults=%+v", got)
	}
	want := Config{Model: "gpt-6-astra", Effort: "high", ContextPolicy: ContextPolicyFull}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round-trip=%+v want=%+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}
func TestLoadNormalizesMissingValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"model":"custom"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "custom" || got.Effort != "medium" || got.ContextPolicy != ContextPolicyFull {
		t.Fatalf("config=%+v", got)
	}
}

func TestContextPolicyValidationAndNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"context_policy":"COMPACT"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.ContextPolicy != ContextPolicyCompact {
		t.Fatalf("compact policy=%+v err=%v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"context_policy":"summarize-history"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unsupported context policy accepted")
	}
	if err := Save(path, Config{ContextPolicy: "summarize-history"}); err == nil {
		t.Fatal("unsupported context policy saved")
	}
}
