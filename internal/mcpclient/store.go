package mcpclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const configVersion = 1
const maxConfigSize = 1 << 20

var environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type ConfigStore struct{ Home string }

type ServerSummary struct {
	ID               string   `json:"id"`
	Command          string   `json:"command"`
	ArgumentsCount   int      `json:"arguments_count"`
	EnvironmentKeys  []string `json:"environment_keys"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
}

type configFile struct {
	Version int            `json:"version"`
	Servers []ServerConfig `json:"servers"`
}

func (s ConfigStore) List() ([]ServerConfig, error) {
	var result []ServerConfig
	err := s.withLock(func() error {
		cfg, err := s.load()
		if err != nil {
			return err
		}
		result = append([]ServerConfig(nil), cfg.Servers...)
		for i := range result {
			result[i].Args = append([]string(nil), result[i].Args...)
			result[i].Env = cloneMap(result[i].Env)
		}
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, err
}

// Summaries returns safe display metadata and never returns environment values
// or argument values, which may contain credentials.
func (s ConfigStore) Summaries() ([]ServerSummary, error) {
	servers, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]ServerSummary, 0, len(servers))
	for _, server := range servers {
		keys := make([]string, 0, len(server.Env))
		for key := range server.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out = append(out, ServerSummary{ID: server.ID, Command: server.Command, ArgumentsCount: len(server.Args), EnvironmentKeys: keys, WorkingDirectory: server.WorkingDirectory})
	}
	return out, nil
}

// Add records an explicitly selected server without launching it. A server is
// started only when a caller creates a run-scoped Host.
func (s ConfigStore) Add(server ServerConfig) error {
	if err := server.validate(); err != nil {
		return err
	}
	return s.withLock(func() error {
		cfg, err := s.load()
		if err != nil {
			return err
		}
		for _, existing := range cfg.Servers {
			if existing.ID == server.ID {
				return fmt.Errorf("MCP server %q is already configured", server.ID)
			}
		}
		server.Args = append([]string(nil), server.Args...)
		server.Env = cloneMap(server.Env)
		cfg.Servers = append(cfg.Servers, server)
		sort.Slice(cfg.Servers, func(i, j int) bool { return cfg.Servers[i].ID < cfg.Servers[j].ID })
		return s.save(cfg)
	})
}

func (s ConfigStore) Remove(id string) error {
	id = strings.TrimSpace(id)
	if !serverIDPattern.MatchString(id) {
		return fmt.Errorf("invalid MCP server id %q", id)
	}
	return s.withLock(func() error {
		cfg, err := s.load()
		if err != nil {
			return err
		}
		index := -1
		for i, server := range cfg.Servers {
			if server.ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("MCP server %q is not configured", id)
		}
		cfg.Servers = append(cfg.Servers[:index], cfg.Servers[index+1:]...)
		return s.save(cfg)
	})
}

func (s ConfigStore) load() (configFile, error) {
	path := s.configPath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return configFile{Version: configVersion, Servers: []ServerConfig{}}, nil
	}
	if err != nil {
		return configFile{}, fmt.Errorf("inspect MCP configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return configFile{}, errors.New("MCP configuration must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return configFile{}, fmt.Errorf("open MCP configuration: %w", err)
	}
	defer file.Close()
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return configFile{}, fmt.Errorf("secure MCP configuration: %w", err)
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConfigSize+1))
	if err != nil {
		return configFile{}, fmt.Errorf("read MCP configuration: %w", err)
	}
	if len(data) > maxConfigSize {
		return configFile{}, errors.New("MCP configuration exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg configFile
	if err := decoder.Decode(&cfg); err != nil {
		return configFile{}, fmt.Errorf("parse MCP configuration: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return configFile{}, errors.New("MCP configuration must contain one JSON object")
	}
	if cfg.Version != configVersion {
		return configFile{}, fmt.Errorf("unsupported MCP configuration version %d", cfg.Version)
	}
	seen := make(map[string]bool)
	for _, server := range cfg.Servers {
		if !serverIDPattern.MatchString(server.ID) || !filepath.IsAbs(server.Command) || seen[server.ID] {
			return configFile{}, errors.New("MCP configuration contains an invalid or duplicate server ID or non-absolute command")
		}
		seen[server.ID] = true
		for key, value := range server.Env {
			if !environmentKeyPattern.MatchString(key) || strings.ContainsRune(value, 0) {
				return configFile{}, fmt.Errorf("MCP server %q has an invalid environment entry", server.ID)
			}
		}
	}
	sort.Slice(cfg.Servers, func(i, j int) bool { return cfg.Servers[i].ID < cfg.Servers[j].ID })
	return cfg, nil
}

func (s ConfigStore) save(cfg configFile) error {
	home, err := s.ensureHome()
	if err != nil {
		return err
	}
	cfg.Version = configVersion
	sort.Slice(cfg.Servers, func(i, j int) bool { return cfg.Servers[i].ID < cfg.Servers[j].ID })
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, ".mcp-*.tmp")
	if err != nil {
		return fmt.Errorf("create MCP configuration temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(append(data, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write MCP configuration: %w", err)
	}
	if err := os.Rename(tmpPath, s.configPath()); err != nil {
		return fmt.Errorf("replace MCP configuration: %w", err)
	}
	return syncMCPDirectory(home)
}

func (s ConfigStore) ensureHome() (string, error) {
	if strings.TrimSpace(s.Home) == "" {
		return "", errors.New("pk home directory is required")
	}
	home, err := filepath.Abs(s.Home)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return "", err
	}
	return home, nil
}

func (s ConfigStore) withLock(fn func() error) error {
	home, err := s.ensureHome()
	if err != nil {
		return err
	}
	unlock, err := lockMCPConfig(filepath.Join(home, "mcp.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func (s ConfigStore) configPath() string {
	home, err := filepath.Abs(s.Home)
	if err != nil {
		home = s.Home
	}
	return filepath.Join(home, "mcp.json")
}

func cloneMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
