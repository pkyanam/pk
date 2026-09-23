package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkyanam/pk/internal/extensions"
	"github.com/pkyanam/pk/internal/interaction"
	pluginconfig "github.com/pkyanam/pk/internal/plugins"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func userPluginService() pluginconfig.Service {
	return pluginconfig.Service{Home: pkHome()}
}

func listRPCPlugins(service pluginconfig.Service) (map[string]any, error) {
	items, err := service.List()
	if err != nil {
		return nil, err
	}
	return map[string]any{"plugins": items}, nil
}

func enableRPCPlugin(service pluginconfig.Service, manifestPath string) (map[string]any, error) {
	if _, err := service.Enable(manifestPath); err != nil {
		return nil, err
	}
	return updatedRPCPlugins(service)
}

func disableRPCPlugin(service pluginconfig.Service, id string) (map[string]any, error) {
	if err := service.Disable(id); err != nil {
		return nil, err
	}
	return updatedRPCPlugins(service)
}

func normalizePluginManifestPath(rawPath, workspace string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", errors.New("plugin manifest path is required")
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		if path == "~" {
			path = userHome()
		} else {
			path = filepath.Join(userHome(), path[2:])
		}
	} else if strings.HasPrefix(path, "~") {
		return "", errors.New("named-user home expansion is not supported; use ~/path")
	}
	if !filepath.IsAbs(path) {
		base := workspace
		if strings.TrimSpace(base) == "" {
			var err error
			base, err = os.Getwd()
			if err != nil {
				return "", fmt.Errorf("resolve current directory: %w", err)
			}
		}
		path = filepath.Join(base, path)
	}
	return filepath.Abs(filepath.Clean(path))
}

func updatedRPCPlugins(service pluginconfig.Service) (map[string]any, error) {
	payload, err := listRPCPlugins(service)
	if err != nil {
		return nil, err
	}
	payload["next_session_only"] = true
	return payload, nil
}

func pluginIssueMessages(issues []error) []string {
	messages := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue != nil {
			messages = append(messages, issue.Error())
		}
	}
	return messages
}

func rpcPluginUnavailableMessage(issues []string) string {
	return "an enabled plugin is unavailable: " + strings.Join(issues, "; ") + ". Build or restore its worker, or disable it with /plugins and start a new session with /new."
}

// snapshotRPCPluginPaths should be called at foreground session start. Keep the
// returned paths on the RPC server for that session so later config changes do
// not silently change its tool schema.
func snapshotRPCPluginPaths(service pluginconfig.Service) ([]string, []error, error) {
	items, err := service.List()
	if err != nil {
		return nil, nil, err
	}
	paths := make([]string, 0, len(items))
	var diagnostics []error
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		if item.Error != "" {
			diagnostics = append(diagnostics, fmt.Errorf("plugin %q: %s", item.ID, item.Error))
			continue
		}
		paths = append(paths, item.ManifestPath)
	}
	return paths, diagnostics, nil
}

// configureRPCPluginSession wires the frozen manifest list into this prompt's
// runner. The host is caller-owned and must be closed after runner.Run returns.
func configureRPCPluginSession(ctx context.Context, options *runner.Options, manifests []string, broker *interaction.Broker, diagnostics io.Writer) (*extensions.Host, error) {
	var extra []cliRegistryExtension
	if broker != nil {
		extra = append(extra, cliRegistryExtension{Decorate: func(base tool.Registry) tool.Registry {
			return interaction.DecorateRegistry(base, broker)
		}})
	}
	return configureCLIExtensions(ctx, options, manifests, nil, diagnostics, extra...)
}

// prepareRPCPluginSession must run after the host is constructed and before
// runner.Run. It verifies resumed sessions and records the frozen schema when
// a new session ID is assigned by the runner. Call the returned finalizer after
// runner.Run to surface sidecar write failures.
func prepareRPCPluginSession(options *runner.Options, host *extensions.Host) (func() error, error) {
	if options == nil {
		return nil, errors.New("runner options are required")
	}
	fingerprint := ""
	if host != nil {
		fingerprint = host.SchemaFingerprint()
	}
	if err := checkPluginSessionFingerprint(options.SessionDir, options.SessionID, fingerprint); err != nil {
		return nil, err
	}
	prior := options.OnSession
	var mu sync.Mutex
	var saveErr error
	options.OnSession = func(id string) {
		if err := savePluginSessionFingerprint(options.SessionDir, id, fingerprint); err != nil {
			mu.Lock()
			if saveErr == nil {
				saveErr = err
			}
			mu.Unlock()
		}
		if prior != nil {
			prior(id)
		}
	}
	return func() error {
		mu.Lock()
		defer mu.Unlock()
		return saveErr
	}, nil
}

type pluginSessionFingerprint struct {
	SessionID  string `json:"session_id"`
	SchemaHash string `json:"schema_hash"`
}

// checkPluginSessionFingerprint rejects an extension set change on resume.
// Existing sessions without a sidecar may be resumed only when no extension is
// currently enabled, preserving compatibility with pre-plugin sessions.
func checkPluginSessionFingerprint(sessionDir, sessionID, currentFingerprint string) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	path := pluginSessionFingerprintPath(sessionDir, sessionID)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if currentFingerprint == "" {
			return nil
		}
		return errors.New("this session has no saved plugin schema; start a new session before enabling plugins")
	}
	if err != nil {
		return fmt.Errorf("read saved plugin schema: %w", err)
	}
	var saved pluginSessionFingerprint
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("parse saved plugin schema: %w", err)
	}
	if saved.SessionID != sessionID || saved.SchemaHash != currentFingerprint {
		return errors.New("enabled plugin schemas differ from this session; restore the original plugins or start a new session")
	}
	return nil
}

func savePluginSessionFingerprint(sessionDir, sessionID, fingerprint string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session ID is required to save plugin schema")
	}
	path := pluginSessionFingerprintPath(sessionDir, sessionID)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(pluginSessionFingerprint{SessionID: sessionID, SchemaHash: fingerprint})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".plugin-schema-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
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
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return nil
}

func pluginSessionFingerprintPath(sessionDir, sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return filepath.Join(sessionDir, "plugin-schemas", hex.EncodeToString(digest[:])+".json")
}
