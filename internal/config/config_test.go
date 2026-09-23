package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	if !reflect.DeepEqual(got, want.Normalized()) {
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

func TestThemeDefaultsValidationAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	got, err := Load(path)
	if err != nil || got.Theme != DefaultTheme {
		t.Fatalf("default theme=%q err=%v", got.Theme, err)
	}
	for _, theme := range []string{ThemeDarkMint, ThemeLight, ThemeHighContrast} {
		cfg := got
		cfg.Theme = theme
		if err := Save(path, cfg); err != nil {
			t.Fatalf("save theme %q: %v", theme, err)
		}
		loaded, err := Load(path)
		if err != nil || loaded.Theme != theme {
			t.Fatalf("round-trip theme=%q err=%v want=%q", loaded.Theme, err, theme)
		}
	}
	if err := Save(path, Config{Theme: "neon"}); err == nil {
		t.Fatal("unsupported theme was saved")
	}
	if err := os.WriteFile(path, []byte(`{"theme":"neon"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unsupported theme was loaded")
	}
}

func TestImageGenDriverOptInRoundTripAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	got, err := Load(path)
	if err != nil || got.ImageGenDriver != "" {
		t.Fatalf("default config=%+v err=%v", got, err)
	}
	want := Defaults()
	want.ImageGenDriver = "gpt-6-astra"
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err = Load(path)
	if err != nil || !reflect.DeepEqual(got, want.Normalized()) {
		t.Fatalf("round-trip=%+v err=%v want=%+v", got, err, want)
	}
	for _, invalid := range []string{"model name", "line\nbreak", strings.Repeat("x", 201)} {
		bad := want
		bad.ImageGenDriver = invalid
		if err := Save(path, bad); err == nil {
			t.Errorf("Save accepted invalid image driver %q", invalid)
		}
	}
}

func TestContextBudgetDefaultsAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"model":"gpt-6-luna"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContextBudget.UnknownInputBudgetTokens == nil || *cfg.ContextBudget.UnknownInputBudgetTokens != DefaultUnknownInputBudgetTokens || cfg.ContextBudget.OutputReserveTokens == nil || *cfg.ContextBudget.OutputReserveTokens != DefaultOutputReserveTokens || cfg.ContextBudget.SafetyMarginTokens == nil || *cfg.ContextBudget.SafetyMarginTokens != DefaultContextSafetyMarginTokens {
		t.Fatalf("legacy config did not receive context budget defaults: %+v", cfg.ContextBudget)
	}
	zero := int64(0)
	cfg.ContextBudget.OutputReserveTokens = &zero
	cfg.ContextBudget.Overrides = []ContextBudgetOverride{{ProviderID: "native", ModelID: "gpt-6-luna", ContextTokens: int64ptr(1_000_000)}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContextBudget.OutputReserveTokens == nil || *got.ContextBudget.OutputReserveTokens != 0 || len(got.ContextBudget.Overrides) != 1 || *got.ContextBudget.Overrides[0].ContextTokens != 1_000_000 {
		t.Fatalf("context budget did not round-trip: %+v", got.ContextBudget)
	}
}

func TestHistoryCompactionDefaultsAndCanBeDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"model":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HistoryCompaction.Enabled == nil || !*cfg.HistoryCompaction.Enabled || *cfg.HistoryCompaction.TriggerRatio != 0.8 || *cfg.HistoryCompaction.TargetRatio != 0.65 || *cfg.HistoryCompaction.MaxSummaryCalls != 6 {
		t.Fatalf("legacy history-compaction defaults=%+v", cfg.HistoryCompaction)
	}
	disabled := false
	cfg.HistoryCompaction.Enabled = &disabled
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.HistoryCompaction.Enabled == nil || *loaded.HistoryCompaction.Enabled {
		t.Fatalf("disabled setting did not persist: %+v err=%v", loaded.HistoryCompaction, err)
	}
}

func TestContextBudgetRejectsInvalidAndDuplicateOverrides(t *testing.T) {
	for _, budget := range []ContextBudgetConfig{
		{UnknownInputBudgetTokens: int64ptr(0)},
		{Overrides: []ContextBudgetOverride{{ProviderID: "native", ModelID: "model"}}},
		{Overrides: []ContextBudgetOverride{{ProviderID: "native", ModelID: "model", ContextTokens: int64ptr(-1)}}},
		{Overrides: []ContextBudgetOverride{{ProviderID: "native", ModelID: "model", ContextTokens: int64ptr(100)}, {ProviderID: "NATIVE", ModelID: "MODEL", InputTokens: int64ptr(50)}}},
	} {
		if err := budget.Validate(); err == nil {
			t.Errorf("invalid context budget accepted: %+v", budget)
		}
	}
}

func int64ptr(value int64) *int64 { return &value }
