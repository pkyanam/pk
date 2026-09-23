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
	DefaultModel          = "gpt-6-luna"
	DefaultEffort         = "medium"
	DefaultImageGenDriver = "gpt-6-astra"
	ContextPolicyFull     = "full"
	ContextPolicyCompact  = "compact"
)

type Config struct {
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	ContextPolicy  string `json:"context_policy"`
	ImageGenDriver string `json:"imagegen_driver,omitempty"`
}

func Defaults() Config {
	return Config{Model: DefaultModel, Effort: DefaultEffort, ContextPolicy: ContextPolicyFull}
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
