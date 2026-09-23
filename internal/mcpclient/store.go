package mcpclient

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
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
const maxSecretStoreSize = 1 << 20

var environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type ConfigStore struct{ Home string }

type ServerSummary struct {
	ID               string   `json:"id"`
	Transport        string   `json:"transport"`
	URL              string   `json:"url,omitempty"`
	AuthMode         string   `json:"auth_mode,omitempty"`
	AuthStatus       string   `json:"auth_status,omitempty"`
	CredentialEnv    []string `json:"credential_env,omitempty"`
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
			result[i].SecretStoreHome = s.Home
		}
		secrets, err := s.loadSecrets()
		if err != nil {
			return err
		}
		for i := range result {
			ref := result[i].Auth.SecretRef
			if ref != "" {
				value, ok := secrets[ref]
				if !ok {
					if result[i].Auth.Mode != "oauth" {
						return fmt.Errorf("MCP credential for server %q is missing; configure it again", result[i].ID)
					}
					value = ""
				}
				result[i].Auth.SecretValue = value
			}
		}
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, err
}

func (s ConfigStore) AddOAuth(server ServerConfig) error {
	if server.URL == "" {
		return errors.New("OAuth requires a remote MCP URL")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("could not create OAuth session reference")
	}
	server.Auth = HTTPAuthConfig{Mode: "oauth", SecretRef: hex.EncodeToString(random[:])}
	if err := server.validate(); err != nil {
		return err
	}
	return s.withLock(func() error {
		cfg, err := s.load()
		if err != nil {
			return err
		}
		for _, x := range cfg.Servers {
			if x.ID == server.ID {
				return fmt.Errorf("MCP server %q is already configured", server.ID)
			}
		}
		secrets, err := s.loadSecrets()
		if err != nil {
			return err
		}
		secrets[server.Auth.SecretRef] = ""
		if err := s.saveSecrets(secrets); err != nil {
			return err
		}
		cfg.Servers = append(cfg.Servers, server)
		if err := s.save(cfg); err != nil {
			delete(secrets, server.Auth.SecretRef)
			_ = s.saveSecrets(secrets)
			return err
		}
		return nil
	})
}

func (s ConfigStore) SetOAuthSession(ref string, session []byte) error {
	if ref == "" || len(session) > 64<<10 {
		return errors.New("invalid OAuth session data")
	}
	return s.withLock(func() error {
		values, err := s.loadSecrets()
		if err != nil {
			return err
		}
		if _, ok := values[ref]; !ok {
			return errors.New("OAuth session reference is unavailable")
		}
		values[ref] = string(session)
		return s.saveSecrets(values)
	})
}

func (s ConfigStore) ClearOAuthSession(ref string) error {
	if ref == "" {
		return errors.New("OAuth session reference is unavailable")
	}
	return s.withLock(func() error {
		values, err := s.loadSecrets()
		if err != nil {
			return err
		}
		if _, ok := values[ref]; !ok {
			return errors.New("OAuth session reference is unavailable")
		}
		values[ref] = ""
		return s.saveSecrets(values)
	})
}

func (s ConfigStore) OAuthSession(ref string) (string, error) {
	var value string
	err := s.withLock(func() error {
		values, err := s.loadSecrets()
		if err != nil {
			return err
		}
		var ok bool
		value, ok = values[ref]
		if !ok {
			return errors.New("OAuth session reference is unavailable")
		}
		return nil
	})
	return value, err
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
		transport, authMode := "stdio", ""
		authStatus := ""
		if server.URL != "" {
			transport, authMode = "streamable_http", server.Auth.Mode
			if server.Auth.Mode == "oauth" {
				if server.Auth.SecretValue != "" {
					authStatus = "authenticated"
				} else {
					authStatus = "needs_login"
				}
			} else if server.Auth.Mode == "bearer_env" || server.Auth.Mode == "header_env" {
				name := server.Auth.BearerEnv
				if server.Auth.Mode == "header_env" {
					name = server.Auth.HeaderValueEnv
				}
				if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "" {
					authStatus = "configured"
				} else {
					authStatus = "needs_credential"
				}
			} else if server.Auth.Mode == "bearer_secret" || server.Auth.Mode == "header_secret" {
				if server.Auth.SecretValue != "" {
					authStatus = "configured"
				} else {
					authStatus = "needs_credential"
				}
			} else if server.Auth.Mode != "" && server.Auth.Mode != "none" {
				authStatus = "configured"
			} else {
				authStatus = "anonymous"
			}
		}
		credentialEnv := []string{}
		if server.Auth.BearerEnv != "" {
			credentialEnv = append(credentialEnv, server.Auth.BearerEnv)
		}
		if server.Auth.HeaderValueEnv != "" {
			credentialEnv = append(credentialEnv, server.Auth.HeaderValueEnv)
		}
		out = append(out, ServerSummary{ID: server.ID, Transport: transport, URL: server.URL, AuthMode: authMode, AuthStatus: authStatus, CredentialEnv: credentialEnv, Command: server.Command, ArgumentsCount: len(server.Args), EnvironmentKeys: keys, WorkingDirectory: server.WorkingDirectory})
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

// AddWithSecret records a remote server and its credential separately. The
// caller must not retain or log secret after this call.
func (s ConfigStore) AddWithSecret(server ServerConfig, kind, secret string) error {
	if server.URL == "" || (kind != "bearer" && kind != "header") || strings.TrimSpace(secret) == "" || strings.ContainsAny(secret, "\r\n\x00") {
		return errors.New("invalid remote MCP credential configuration")
	}
	if kind == "bearer" {
		server.Auth = HTTPAuthConfig{Mode: "bearer_secret"}
	} else {
		if !validHeaderName(server.Auth.HeaderName) {
			return errors.New("invalid HTTP credential header name")
		}
		name := server.Auth.HeaderName
		server.Auth = HTTPAuthConfig{Mode: "header_secret", HeaderName: name}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("could not create MCP credential reference")
	}
	server.Auth.SecretRef = hex.EncodeToString(random[:])
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
		secrets, err := s.loadSecrets()
		if err != nil {
			return err
		}
		secrets[server.Auth.SecretRef] = secret
		if err := s.saveSecrets(secrets); err != nil {
			return err
		}
		cfg.Servers = append(cfg.Servers, server)
		if err := s.save(cfg); err != nil {
			delete(secrets, server.Auth.SecretRef)
			_ = s.saveSecrets(secrets)
			return err
		}
		return nil
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
		ref := ""
		for i, server := range cfg.Servers {
			if server.ID == id {
				index = i
				ref = server.Auth.SecretRef
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("MCP server %q is not configured", id)
		}
		cfg.Servers = append(cfg.Servers[:index], cfg.Servers[index+1:]...)
		if err := s.save(cfg); err != nil {
			return err
		}
		if ref != "" {
			secrets, err := s.loadSecrets()
			if err != nil {
				return err
			}
			delete(secrets, ref)
			return s.saveSecrets(secrets)
		}
		return nil
	})
}

func (s ConfigStore) loadSecrets() (map[string]string, error) {
	path := filepath.Join(s.Home, "mcp-secrets.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect MCP credential store: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("MCP credential store must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure MCP credential store: %w", err)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open MCP credential store: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSecretStoreSize+1))
	if err != nil {
		return nil, fmt.Errorf("read MCP credential store: %w", err)
	}
	if len(data) > maxSecretStoreSize {
		return nil, errors.New("MCP credential store exceeds 1 MiB")
	}
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil || values == nil {
		return nil, errors.New("parse MCP credential store")
	}
	return values, nil
}

func (s ConfigStore) saveSecrets(values map[string]string) error {
	home, err := s.ensureHome()
	if err != nil {
		return err
	}
	data, err := json.Marshal(values)
	if err != nil {
		return errors.New("encode MCP credential store")
	}
	if len(data) > maxSecretStoreSize {
		return errors.New("MCP credential store exceeds 1 MiB")
	}
	tmp, err := os.CreateTemp(home, ".mcp-secrets-*.tmp")
	if err != nil {
		return fmt.Errorf("create MCP credential store temp file: %w", err)
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
		return fmt.Errorf("write MCP credential store: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(home, "mcp-secrets.json")); err != nil {
		return fmt.Errorf("replace MCP credential store: %w", err)
	}
	return syncMCPDirectory(home)
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
		if !serverIDPattern.MatchString(server.ID) || seen[server.ID] {
			return configFile{}, errors.New("MCP configuration contains an invalid or duplicate server ID")
		}
		if err := server.validate(); err != nil {
			return configFile{}, fmt.Errorf("invalid MCP configuration for %q: %w", server.ID, err)
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
