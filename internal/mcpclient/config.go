// Package mcpclient connects only to MCP servers named in explicit user
// configuration. It never scans a workspace for server configuration.
package mcpclient

import (
	"fmt"
	"net"
	"net/url"
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
	URL              string            `json:"url,omitempty"`
	Auth             HTTPAuthConfig    `json:"auth,omitempty"`
	Command          string            `json:"command"`
	Args             []string          `json:"args,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	OAuthLogin       bool              `json:"-"`
	SecretStoreHome  string            `json:"-"`
}

// HTTPAuthConfig names environment variables containing credentials. Secret
// values are never persisted in the MCP config file.
type HTTPAuthConfig struct {
	Mode           string `json:"mode,omitempty"` // "bearer_env" or "header_env"
	BearerEnv      string `json:"bearer_env,omitempty"`
	HeaderName     string `json:"header_name,omitempty"`
	HeaderValueEnv string `json:"header_value_env,omitempty"`
	SecretRef      string `json:"secret_ref,omitempty"`
	SecretValue    string `json:"-"`
}

func (c ServerConfig) validate() error {
	if !serverIDPattern.MatchString(c.ID) {
		return fmt.Errorf("invalid MCP server id %q", c.ID)
	}
	if c.URL != "" {
		if c.Command != "" || len(c.Args) > 0 || len(c.Env) > 0 || c.WorkingDirectory != "" {
			return fmt.Errorf("MCP server %q must configure either a URL or a stdio command", c.ID)
		}
		if err := validateMCPURL(c.URL); err != nil {
			return fmt.Errorf("MCP server %q: %w", c.ID, err)
		}
		if err := c.Auth.validate(); err != nil {
			return fmt.Errorf("MCP server %q: %w", c.ID, err)
		}
	} else {
		if c.Auth != (HTTPAuthConfig{}) {
			return fmt.Errorf("MCP server %q HTTP auth requires a URL", c.ID)
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

func validateMCPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return fmt.Errorf("URL must be an absolute HTTP(S) endpoint without credentials, query, or fragment")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("URL must use HTTPS (HTTP is allowed only for loopback fixtures)")
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if !(strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())) {
			return fmt.Errorf("unencrypted HTTP is allowed only for loopback addresses")
		}
	}
	return nil
}

func (a HTTPAuthConfig) validate() error {
	switch a.Mode {
	case "", "none":
		if a.BearerEnv != "" || a.HeaderName != "" || a.HeaderValueEnv != "" || a.SecretRef != "" {
			return fmt.Errorf("auth fields require a matching auth mode")
		}
	case "bearer_env":
		if !environmentKeyPattern.MatchString(a.BearerEnv) || a.HeaderName != "" || a.HeaderValueEnv != "" || a.SecretRef != "" {
			return fmt.Errorf("bearer_env mode requires a valid bearer_env variable name")
		}
	case "header_env":
		if !environmentKeyPattern.MatchString(a.HeaderValueEnv) || !validHeaderName(a.HeaderName) || strings.EqualFold(a.HeaderName, "Host") || strings.EqualFold(a.HeaderName, "Cookie") || a.SecretRef != "" {
			return fmt.Errorf("header_env mode requires a valid header name and header_value_env variable")
		}
	case "bearer_secret":
		if a.SecretRef == "" || a.BearerEnv != "" || a.HeaderName != "" || a.HeaderValueEnv != "" {
			return fmt.Errorf("bearer_secret mode requires a stored secret reference")
		}
	case "header_secret":
		if a.SecretRef == "" || !validHeaderName(a.HeaderName) || strings.EqualFold(a.HeaderName, "Host") || strings.EqualFold(a.HeaderName, "Cookie") {
			return fmt.Errorf("header_secret mode requires a stored secret reference and valid header name")
		}
	case "oauth":
		if a.SecretRef == "" || a.BearerEnv != "" || a.HeaderName != "" || a.HeaderValueEnv != "" {
			return fmt.Errorf("oauth mode requires an OAuth session reference")
		}
	default:
		return fmt.Errorf("unsupported HTTP auth mode %q", a.Mode)
	}
	return nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
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
