package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/config"
)

func TestContextBudgetConfigCommandRoundTripAndClear(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	var out, errOut bytes.Buffer
	code := runContextBudgetConfigCommand(filepath.Join(home, "config.json"), []string{"set", "--provider", "native", "--model", "gpt-6-luna", "--context", "1000000", "--input", "900000", "--output", "100000", "--reserve", "24000", "--margin", "4000", "--unknown-input", "128000"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("set returned %d: %s", code, errOut.String())
	}
	cfg, err := config.Load(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ContextBudget.Overrides) != 1 || *cfg.ContextBudget.Overrides[0].ContextTokens != 1_000_000 || *cfg.ContextBudget.Overrides[0].InputTokens != 900_000 || *cfg.ContextBudget.Overrides[0].OutputTokens != 100_000 || *cfg.ContextBudget.OutputReserveTokens != 24_000 || *cfg.ContextBudget.SafetyMarginTokens != 4_000 {
		t.Fatalf("saved budget=%+v", cfg.ContextBudget)
	}
	out.Reset()
	errOut.Reset()
	if code := runContextBudgetConfigCommand(filepath.Join(home, "config.json"), []string{"show"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "override native/gpt-6-luna") {
		t.Fatalf("show returned %d output=%q err=%q", code, out.String(), errOut.String())
	}
	if code := runContextBudgetConfigCommand(filepath.Join(home, "config.json"), []string{"set", "--provider", "native", "--model", "gpt-6-luna", "--context", "0", "--input", "0", "--output", "0"}, &out, &errOut); code != 0 {
		t.Fatalf("clear returned %d: %s", code, errOut.String())
	}
	cfg, err = config.Load(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ContextBudget.Overrides) != 0 {
		t.Fatalf("cleared override remained: %+v", cfg.ContextBudget.Overrides)
	}
	info, err := os.Stat(filepath.Join(home, "config.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode=%v err=%v", info, err)
	}
}

func TestContextBudgetConfigCommandRejectsInvalidNumbers(t *testing.T) {
	for _, args := range [][]string{
		{"set", "--provider", "native", "--model", "m", "--context", "-1"},
		{"set", "--unknown-input", "0"},
		{"set", "--provider", "native", "--context", "100"},
		{"set", "--provider", "native", "--model", "m"},
	} {
		var out, errOut bytes.Buffer
		code := runContextBudgetConfigCommand(filepath.Join(t.TempDir(), "config.json"), args, &out, &errOut)
		if code != 2 {
			t.Errorf("args=%v returned %d; expected usage/validation error", args, code)
		}
	}
}

func TestContextBudgetJSONDoesNotIncludeUnknownSourceMetadata(t *testing.T) {
	data, err := json.Marshal(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "source") {
		t.Fatalf("source/provenance should be resolved at runtime, config=%s", data)
	}
}
