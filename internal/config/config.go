// Package config stores user-level pk defaults.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	DefaultModel                             = "gpt-6-luna"
	DefaultEffort                            = "medium"
	DefaultImageGenDriver                    = "gpt-6-astra"
	ThemeDarkMint                            = "dark-mint"
	ThemeLight                               = "light"
	ThemeHighContrast                        = "high-contrast"
	DefaultTheme                             = ThemeDarkMint
	ContextPolicyFull                        = "full"
	ContextPolicyCompact                     = "compact"
	DefaultUnknownInputBudgetTokens    int64 = 128_000
	DefaultOutputReserveTokens         int64 = 25_000
	DefaultContextSafetyMarginTokens   int64 = 4_096
	maxContextBudgetTokens             int64 = 10_000_000
	defaultHistoryCompactionTrigger          = 0.80
	defaultHistoryCompactionTarget           = 0.65
	defaultHistorySummaryReserveTokens int64 = 5_000
	defaultHistorySummaryInputTokens   int64 = 12_000
	defaultHistoryMaxSummaryTokens     int64 = 2_500
	defaultHistoryMaxSummaryCalls      int64 = 6
)

type Config struct {
	Model             string                  `json:"model"`
	Effort            string                  `json:"effort"`
	ContextPolicy     string                  `json:"context_policy"`
	ImageGenDriver    string                  `json:"imagegen_driver,omitempty"`
	ContextBudget     ContextBudgetConfig     `json:"context_budget,omitempty"`
	HistoryCompaction HistoryCompactionConfig `json:"history_compaction,omitempty"`
	Theme             string                  `json:"theme,omitempty"`
}

type HistoryCompactionConfig struct {
	Enabled              *bool    `json:"enabled,omitempty"`
	TriggerRatio         *float64 `json:"trigger_ratio,omitempty"`
	TargetRatio          *float64 `json:"target_ratio,omitempty"`
	SummaryReserveTokens *int64   `json:"summary_reserve_tokens,omitempty"`
	SummaryInputTokens   *int64   `json:"summary_input_tokens,omitempty"`
	MaxSummaryTokens     *int64   `json:"max_summary_tokens,omitempty"`
	MaxSummaryCalls      *int64   `json:"max_summary_calls,omitempty"`
}

// ContextBudgetConfig controls window limits and operational reserves. It is
// deliberately separate from ContextPolicy, which controls captured Bash
// result presentation.
type ContextBudgetConfig struct {
	UnknownInputBudgetTokens *int64                  `json:"unknown_input_budget_tokens,omitempty"`
	OutputReserveTokens      *int64                  `json:"output_reserve_tokens,omitempty"`
	SafetyMarginTokens       *int64                  `json:"safety_margin_tokens,omitempty"`
	Overrides                []ContextBudgetOverride `json:"overrides,omitempty"`
}

type ContextBudgetOverride struct {
	ProviderID    string `json:"provider_id"`
	ModelID       string `json:"model_id"`
	ContextTokens *int64 `json:"context_tokens,omitempty"`
	InputTokens   *int64 `json:"input_tokens,omitempty"`
	OutputTokens  *int64 `json:"output_tokens,omitempty"`
}

func Defaults() Config {
	return Config{Model: DefaultModel, Effort: DefaultEffort, ContextPolicy: ContextPolicyFull, Theme: DefaultTheme, ContextBudget: DefaultContextBudgetConfig(), HistoryCompaction: DefaultHistoryCompactionConfig()}
}

func DefaultContextBudgetConfig() ContextBudgetConfig {
	unknown, reserve, margin := DefaultUnknownInputBudgetTokens, DefaultOutputReserveTokens, DefaultContextSafetyMarginTokens
	return ContextBudgetConfig{UnknownInputBudgetTokens: &unknown, OutputReserveTokens: &reserve, SafetyMarginTokens: &margin}
}

func DefaultHistoryCompactionConfig() HistoryCompactionConfig {
	enabled := true
	trigger, target := defaultHistoryCompactionTrigger, defaultHistoryCompactionTarget
	reserve, input, output, calls := defaultHistorySummaryReserveTokens, defaultHistorySummaryInputTokens, defaultHistoryMaxSummaryTokens, defaultHistoryMaxSummaryCalls
	return HistoryCompactionConfig{Enabled: &enabled, TriggerRatio: &trigger, TargetRatio: &target, SummaryReserveTokens: &reserve, SummaryInputTokens: &input, MaxSummaryTokens: &output, MaxSummaryCalls: &calls}
}

func (c HistoryCompactionConfig) Normalized() HistoryCompactionConfig {
	defaults := DefaultHistoryCompactionConfig()
	if c.Enabled == nil {
		c.Enabled = defaults.Enabled
	}
	if c.TriggerRatio == nil {
		c.TriggerRatio = defaults.TriggerRatio
	}
	if c.TargetRatio == nil {
		c.TargetRatio = defaults.TargetRatio
	}
	if c.SummaryReserveTokens == nil {
		c.SummaryReserveTokens = defaults.SummaryReserveTokens
	}
	if c.SummaryInputTokens == nil {
		c.SummaryInputTokens = defaults.SummaryInputTokens
	}
	if c.MaxSummaryTokens == nil {
		c.MaxSummaryTokens = defaults.MaxSummaryTokens
	}
	if c.MaxSummaryCalls == nil {
		c.MaxSummaryCalls = defaults.MaxSummaryCalls
	}
	return c
}

func (c HistoryCompactionConfig) Validate() error {
	c = c.Normalized()
	if c.TriggerRatio == nil || c.TargetRatio == nil || *c.TargetRatio <= 0 || *c.TriggerRatio >= 1 || *c.TargetRatio >= *c.TriggerRatio {
		return errors.New("history compaction ratios must satisfy 0 < target < trigger < 1")
	}
	for _, value := range []*int64{c.SummaryReserveTokens, c.SummaryInputTokens, c.MaxSummaryTokens} {
		if value == nil || *value < 0 || *value > maxContextBudgetTokens {
			return errors.New("history compaction token limits must be between 0 and 10000000")
		}
	}
	if c.MaxSummaryCalls == nil || *c.MaxSummaryCalls < 0 || *c.MaxSummaryCalls > 100 {
		return errors.New("history compaction max_summary_calls must be between 0 and 100")
	}
	return nil
}

func (c ContextBudgetConfig) Normalized() ContextBudgetConfig {
	defaults := DefaultContextBudgetConfig()
	if c.UnknownInputBudgetTokens == nil {
		c.UnknownInputBudgetTokens = defaults.UnknownInputBudgetTokens
	}
	if c.OutputReserveTokens == nil {
		c.OutputReserveTokens = defaults.OutputReserveTokens
	}
	if c.SafetyMarginTokens == nil {
		c.SafetyMarginTokens = defaults.SafetyMarginTokens
	}
	return c
}

func (c ContextBudgetConfig) Validate() error {
	c = c.Normalized()
	if c.UnknownInputBudgetTokens == nil || *c.UnknownInputBudgetTokens <= 0 || *c.UnknownInputBudgetTokens > maxContextBudgetTokens {
		return fmt.Errorf("invalid unknown_input_budget_tokens (use 1..%d)", maxContextBudgetTokens)
	}
	if !validBudgetNumber(c.OutputReserveTokens) || !validBudgetNumber(c.SafetyMarginTokens) {
		return fmt.Errorf("context budget reserve/margin must be between 0 and %d tokens", maxContextBudgetTokens)
	}
	seen := make(map[string]struct{}, len(c.Overrides))
	if len(c.Overrides) > 128 {
		return errors.New("context budget supports at most 128 model overrides")
	}
	for _, override := range c.Overrides {
		if !validContextBudgetKey(override.ProviderID) || !validContextBudgetKey(override.ModelID) {
			return errors.New("context budget override requires a valid provider_id and model_id")
		}
		key := strings.ToLower(override.ProviderID) + "\x00" + strings.ToLower(override.ModelID)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate context budget override for %s/%s", override.ProviderID, override.ModelID)
		}
		seen[key] = struct{}{}
		if override.ContextTokens == nil && override.InputTokens == nil && override.OutputTokens == nil {
			return fmt.Errorf("context budget override for %s/%s has no limits", override.ProviderID, override.ModelID)
		}
		for _, value := range []*int64{override.ContextTokens, override.InputTokens, override.OutputTokens} {
			if value != nil && (*value <= 0 || *value > maxContextBudgetTokens) {
				return fmt.Errorf("context budget model limits must be between 1 and %d tokens", maxContextBudgetTokens)
			}
		}
	}
	return nil
}

func validBudgetNumber(value *int64) bool {
	return value != nil && *value >= 0 && *value <= maxContextBudgetTokens
}

func validContextBudgetKey(value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > 200 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func ValidEffort(effort string) bool {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}
func ValidContextPolicy(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case ContextPolicyFull, ContextPolicyCompact:
		return true
	default:
		return false
	}
}

func ValidTheme(theme string) bool {
	switch strings.ToLower(strings.TrimSpace(theme)) {
	case ThemeDarkMint, ThemeLight, ThemeHighContrast:
		return true
	default:
		return false
	}
}

func (c Config) Normalized() Config {
	if strings.TrimSpace(c.Model) == "" {
		c.Model = DefaultModel
	}
	if strings.TrimSpace(c.Effort) == "" {
		c.Effort = DefaultEffort
	} else {
		c.Effort = strings.ToLower(strings.TrimSpace(c.Effort))
	}
	if strings.TrimSpace(c.ContextPolicy) == "" {
		c.ContextPolicy = ContextPolicyFull
	} else {
		c.ContextPolicy = strings.ToLower(strings.TrimSpace(c.ContextPolicy))
	}
	c.ImageGenDriver = strings.TrimSpace(c.ImageGenDriver)
	if strings.TrimSpace(c.Theme) == "" {
		c.Theme = DefaultTheme
	} else {
		c.Theme = strings.ToLower(strings.TrimSpace(c.Theme))
	}
	c.ContextBudget = c.ContextBudget.Normalized()
	c.HistoryCompaction = c.HistoryCompaction.Normalized()
	return c
}
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	c = c.Normalized()
	if !ValidContextPolicy(c.ContextPolicy) {
		return Config{}, fmt.Errorf("invalid context_policy %q (use full or compact)", c.ContextPolicy)
	}
	if !ValidImageGenDriver(c.ImageGenDriver) {
		return Config{}, fmt.Errorf("invalid imagegen_driver %q", c.ImageGenDriver)
	}
	if !ValidTheme(c.Theme) {
		return Config{}, fmt.Errorf("invalid theme %q (use %s, %s, or %s)", c.Theme, ThemeDarkMint, ThemeLight, ThemeHighContrast)
	}
	if err := c.ContextBudget.Validate(); err != nil {
		return Config{}, err
	}
	if err := c.HistoryCompaction.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}
func Save(path string, c Config) error {
	c = c.Normalized()
	if !ValidContextPolicy(c.ContextPolicy) {
		return fmt.Errorf("invalid context_policy %q (use full or compact)", c.ContextPolicy)
	}
	if !ValidImageGenDriver(c.ImageGenDriver) {
		return fmt.Errorf("invalid imagegen_driver %q", c.ImageGenDriver)
	}
	if !ValidTheme(c.Theme) {
		return fmt.Errorf("invalid theme %q (use %s, %s, or %s)", c.Theme, ThemeDarkMint, ThemeLight, ThemeHighContrast)
	}
	if err := c.ContextBudget.Validate(); err != nil {
		return err
	}
	if err := c.HistoryCompaction.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ValidImageGenDriver accepts an empty value (disabled) or a bounded model ID
// made from printable non-whitespace characters. The value is passed as one
// argv element to the Codex CLI image worker; it is never interpreted by a shell.
func ValidImageGenDriver(driver string) bool {
	driver = strings.TrimSpace(driver)
	if driver == "" {
		return true
	}
	if len(driver) > 200 {
		return false
	}
	for _, r := range driver {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}
