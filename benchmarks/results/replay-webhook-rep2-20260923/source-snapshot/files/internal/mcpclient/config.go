// Package mcpclient connects only to MCP servers named in explicit user
// configuration. It never scans a workspace for server configuration.
package mcpclient

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	maxServers        = 32
	maxToolsPerServer = 256
	maxSchemaBytes    = 64 << 10
	maxToolResult     = 256 << 10
	maxDescription    = 4 << 10
)

var serverIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,31}$`)

// ServerConfig is a user-approved stdio server declaration. Command must be
// absolute. The process receives only a small baseline environment plus Env.
// If WorkingDirectory is empty, the server runs from the user's home directory,
// never from the pk workspace by default.
type ServerConfig struct {
	ID               string            `json:"id"`
	Command          string            `json:"command"`
	Args             []string          `json:"args,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
}

func (c ServerConfig) validate() error {
	if !serverIDPattern.MatchString(c.ID) {
		return fmt.Errorf("invalid MCP server id %q", c.ID)
	}
	if !filepath.IsAbs(c.Command) {
		return fmt.Errorf("MCP server %q command must be an absolute path", c.ID)
	}
	info, err := os.Stat(c.Command)
	if err != nil {
		return fmt.Errorf("inspect MCP server %q command: %w", c.ID, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("MCP server %q command must be a regular file", c.ID)
	}
	if c.WorkingDirectory != "" {
		if !filepath.IsAbs(c.WorkingDirectory) {
			return fmt.Errorf("MCP server %q working directory must be absolute", c.ID)
		}
		info, err := os.Stat(c.WorkingDirectory)
		if err != nil {
			return fmt.Errorf("inspect MCP server %q working directory: %w", c.ID, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("MCP server %q working directory must be a directory", c.ID)
		}
	}
	for key, value := range c.Env {
		if !environmentKeyPattern.MatchString(key) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("MCP server %q has an invalid environment entry", c.ID)
		}
	}
	for _, arg := range c.Args {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("MCP server %q has an argument containing NUL", c.ID)
		}
	}
	return nil
}

func processEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "SYSTEMROOT", "WINDIR", "LANG"} {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	if _, ok := values["HOME"]; !ok {
		if home, err := os.UserHomeDir(); err == nil {
			values["HOME"] = home
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	sort.Strings(out)
	return out
}
