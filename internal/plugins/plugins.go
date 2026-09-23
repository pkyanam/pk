// Package plugins stores user-selected extension manifest paths. It does not
// discover or execute manifests; callers load only the enabled paths returned
// by EnabledManifests.
package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/pkyanam/pk/internal/extensions"
)

const configVersion = 1
const maxConfigSize = 1 << 20

var pluginIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

type Service struct {
	// Home is the private pk home directory, typically the value of PK_HOME.
	Home string
}

type Plugin struct {
	ID           string   `json:"id"`
	Version      string   `json:"version,omitempty"`
	ManifestPath string   `json:"manifest_path"`
	Enabled      bool     `json:"enabled"`
	Tools        []string `json:"tools,omitempty"`
	Commands     []string `json:"commands,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type config struct {
	Version int     `json:"version"`
	Plugins []entry `json:"plugins"`
}

type entry struct {
	ID           string `json:"id"`
	ManifestPath string `json:"manifest_path"`
	Enabled      bool   `json:"enabled"`
}

func (s Service) List() ([]Plugin, error) {
	var result []Plugin
	err := s.withLock(func() error {
		cfg, err := s.load()
		if err != nil {
			return err
		}
		result = make([]Plugin, 0, len(cfg.Plugins))
		toolOwners := map[string]string{}
		for _, item := range cfg.Plugins {
			plugin := Plugin{ID: item.ID, ManifestPath: item.ManifestPath, Enabled: item.Enabled}
			manifest, err := extensions.LoadManifest(item.ManifestPath)
			if err != nil {
				plugin.Error = err.Error()
			} else if manifest.ID != item.ID {
				plugin.Error = fmt.Sprintf("manifest now declares ID %q", manifest.ID)
			} else {
				plugin.Version = manifest.Version
				for _, tool := range manifest.Tools {
					plugin.Tools = append(plugin.Tools, tool.Name)
				}
				for _, command := range manifest.Commands {
					plugin.Commands = append(plugin.Commands, command.Name)
				}
				if err := validateWorkerExecutable(manifest.Executable); err != nil {
					plugin.Error = fmt.Sprintf("worker unavailable: %v", err)
				}
				if item.Enabled {
					for _, spec := range manifest.Tools {
						if plugin.Error != "" {
							break
						}
						if owner := toolOwners[spec.Name]; owner != "" {
							plugin.Error = fmt.Sprintf("tool %q conflicts with plugin %q", spec.Name, owner)
							break
						}
					}
					if plugin.Error == "" {
						for _, spec := range manifest.Tools {
							toolOwners[spec.Name] = item.ID
						}
					}
				}
			}
			result = append(result, plugin)
		}
		return nil
	})
	return result, err
}

// Enable adds a manifest by explicit path or re-enables its existing entry.
// It validates every enabled manifest and rejects duplicate IDs or model-tool
// names before atomically persisting the new configuration. Slash commands are
// namespaced by extension ID and may reuse a leaf name.
func (s Service) Enable(manifestPath string) (Plugin, error) {
	manifest, path, err := loadExplicit(manifestPath)
	if err != nil {
		return Plugin{}, err
	}
	var result Plugin
	err = s.withLock(func() error {
		cfg, err := s.load()
		if err != nil {
			return err
		}
		found := false
		for i := range cfg.Plugins {
			item := &cfg.Plugins[i]
			if item.ManifestPath == path && item.ID != manifest.ID {
				return fmt.Errorf("manifest path %s is already registered as plugin %q", path, item.ID)
			}
			if item.ID == manifest.ID {
				if item.ManifestPath != path {
					return fmt.Errorf("plugin ID %q is already registered at %s", manifest.ID, item.ManifestPath)
				}
				item.Enabled = true
				found = true
			}
		}
		if !found {
			cfg.Plugins = append(cfg.Plugins, entry{ID: manifest.ID, ManifestPath: path, Enabled: true})
		}
		if err := validateEnabled(cfg, &manifest, path); err != nil {
			return err
		}
		sortEntries(cfg.Plugins)
		if err := s.save(cfg); err != nil {
			return err
		}
		result = describe(manifest, path, true)
		return nil
	})
	return result, err
}

func (s Service) Disable(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("plugin ID is required")
	}
	return s.withLock(func() error {
		cfg, err := s.load()
		if err != nil {
			return err
		}
		found := false
		for i := range cfg.Plugins {
			if cfg.Plugins[i].ID == id {
				cfg.Plugins[i].Enabled = false
				found = true
			}
		}
		if !found {
			return fmt.Errorf("plugin %q is not registered", id)
		}
		return s.save(cfg)
	})
}

// Remove forgets a plugin ID atomically. Managed content under Home/plugins/ID
// is removed after the config update; explicit external manifest paths are
// never deleted by this method.
func (s Service) Remove(id string) error {
	id = strings.TrimSpace(id)
	if !pluginIDPattern.MatchString(id) {
		return errors.New("plugin ID is required and must be a valid plugin ID")
	}
	return s.withLock(func() error {
		cfg, err := s.load()
		if err != nil {
			return err
		}
		index := -1
		for i := range cfg.Plugins {
			if cfg.Plugins[i].ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("plugin %q is not registered", id)
		}
		manifestPath := cfg.Plugins[index].ManifestPath
		cfg.Plugins = append(cfg.Plugins[:index], cfg.Plugins[index+1:]...)
		if err := s.save(cfg); err != nil {
			return err
		}
		managedRoot := filepath.Join(s.configPathHome(), "plugins")
		managedDir := filepath.Join(managedRoot, id)
		if !pathWithin(managedDir, manifestPath) {
			return nil // external manifest entry: forget-only.
		}
		info, err := os.Lstat(managedDir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect managed plugin directory: %w", err)
		}
		rootInfo, err := os.Lstat(managedRoot)
		if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("plugin entry was removed but managed plugin root is not a real directory; files were left in place")
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed plugin path is not a real directory; config entry was removed but files were left in place")
		}
		if err := os.RemoveAll(managedDir); err != nil {
			return fmt.Errorf("plugin entry was removed but managed files could not be deleted: %w", err)
		}
		return nil
	})
}

func (s Service) configPathHome() string {
	home, err := filepath.Abs(s.Home)
	if err != nil {
		return s.Home
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		return resolved
	}
	return home
}

func pathWithin(root, target string) bool {
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return false
	}
	if resolved, resolveErr := filepath.EvalSymlinks(target); resolveErr == nil {
		target = resolved
	}
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// EnabledManifests returns only explicitly enabled, currently valid manifests.
// Invalid entries are returned as diagnostics while valid entries remain usable.
func (s Service) EnabledManifests() ([]extensions.Manifest, []error, error) {
	var manifests []extensions.Manifest
	var issues []error
	err := s.withLock(func() error {
		cfg, err := s.load()
		if err != nil {
			return err
		}
		var loaded []struct {
			entry    entry
			manifest extensions.Manifest
		}
		for _, item := range cfg.Plugins {
			if !item.Enabled {
				continue
			}
			manifest, err := extensions.LoadManifest(item.ManifestPath)
			if err != nil {
				issues = append(issues, fmt.Errorf("plugin %q: %w", item.ID, err))
				continue
			}
			if manifest.ID != item.ID {
				issues = append(issues, fmt.Errorf("plugin %q manifest now declares ID %q", item.ID, manifest.ID))
				continue
			}
			if err := validateWorkerExecutable(manifest.Executable); err != nil {
				issues = append(issues, fmt.Errorf("plugin %q worker unavailable: %w", item.ID, err))
				continue
			}
			loaded = append(loaded, struct {
				entry    entry
				manifest extensions.Manifest
			}{item, manifest})
		}
		toolOwners := map[string]string{}
		for _, item := range loaded { // config is sorted by ID, so winners are stable
			conflict := ""
			for _, spec := range item.manifest.Tools {
				if owner := toolOwners[spec.Name]; owner != "" {
					conflict = fmt.Sprintf("tool %q conflicts with plugin %q", spec.Name, owner)
					break
				}
			}
			if conflict != "" {
				issues = append(issues, fmt.Errorf("plugin %q: %s; plugin disabled for this session", item.entry.ID, conflict))
				continue
			}
			for _, spec := range item.manifest.Tools {
				toolOwners[spec.Name] = item.entry.ID
			}
			manifests = append(manifests, item.manifest)
		}
		return nil
	})
	return manifests, issues, err
}

func loadExplicit(path string) (extensions.Manifest, string, error) {
	if strings.TrimSpace(path) == "" {
		return extensions.Manifest{}, "", errors.New("plugin manifest path is required")
	}
	if !filepath.IsAbs(path) {
		return extensions.Manifest{}, "", errors.New("plugin manifest path must be absolute")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return extensions.Manifest{}, "", fmt.Errorf("resolve plugin manifest path: %w", err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return extensions.Manifest{}, "", fmt.Errorf("resolve plugin manifest path: %w", err)
	}
	manifest, err := extensions.LoadManifest(abs)
	if err != nil {
		return extensions.Manifest{}, "", err
	}
	if err := validateWorkerExecutable(manifest.Executable); err != nil {
		return extensions.Manifest{}, "", fmt.Errorf("plugin %q worker is unavailable: %w", manifest.ID, err)
	}
	return manifest, abs, nil
}

func validateWorkerExecutable(executable string) error {
	resolved := executable
	if !filepath.IsAbs(resolved) {
		path, err := exec.LookPath(resolved)
		if err != nil {
			return fmt.Errorf("executable %q was not found in PATH", resolved)
		}
		resolved = path
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("inspect %q: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%q is not a regular file", resolved)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%q is not executable", resolved)
	}
	return nil
}

func validateEnabled(cfg config, candidate *extensions.Manifest, candidatePath string) error {
	toolOwners, ids := map[string]string{}, map[string]string{}
	for _, item := range cfg.Plugins {
		if !item.Enabled {
			continue
		}
		manifest := *candidate
		if item.ManifestPath != candidatePath {
			var err error
			manifest, err = extensions.LoadManifest(item.ManifestPath)
			if err != nil {
				return fmt.Errorf("enabled plugin %q is invalid: %w", item.ID, err)
			}
		}
		if manifest.ID != item.ID {
			return fmt.Errorf("plugin entry %q manifest declares ID %q", item.ID, manifest.ID)
		}
		if owner := ids[manifest.ID]; owner != "" && owner != item.ManifestPath {
			return fmt.Errorf("duplicate plugin ID %q", manifest.ID)
		}
		ids[manifest.ID] = item.ManifestPath
		for _, spec := range manifest.Tools {
			if owner := toolOwners[spec.Name]; owner != "" && owner != manifest.ID {
				return fmt.Errorf("plugin tool %q conflicts between %q and %q", spec.Name, owner, manifest.ID)
			}
			toolOwners[spec.Name] = manifest.ID
		}
	}
	return nil
}

func describe(manifest extensions.Manifest, path string, enabled bool) Plugin {
	plugin := Plugin{ID: manifest.ID, Version: manifest.Version, ManifestPath: path, Enabled: enabled}
	for _, spec := range manifest.Tools {
		plugin.Tools = append(plugin.Tools, spec.Name)
	}
	for _, spec := range manifest.Commands {
		plugin.Commands = append(plugin.Commands, spec.Name)
	}
	return plugin
}

func (s Service) load() (config, error) {
	path := s.configPath()
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return config{Version: configVersion, Plugins: []entry{}}, nil
	}
	if err != nil {
		return config{}, fmt.Errorf("inspect plugin configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return config{}, errors.New("plugin configuration must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return config{}, fmt.Errorf("open plugin configuration: %w", err)
	}
	defer file.Close()
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return config{}, fmt.Errorf("secure plugin configuration: %w", err)
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConfigSize+1))
	if err != nil {
		return config{}, err
	}
	if len(data) > maxConfigSize {
		return config{}, errors.New("plugin configuration exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg config
	if err := decoder.Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("parse plugin configuration: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return config{}, errors.New("plugin configuration must contain one JSON object")
	}
	if cfg.Version != configVersion {
		return config{}, fmt.Errorf("unsupported plugin configuration version %d", cfg.Version)
	}
	seen := map[string]bool{}
	for _, item := range cfg.Plugins {
		if !pluginIDPattern.MatchString(item.ID) || !filepath.IsAbs(item.ManifestPath) {
			return config{}, errors.New("plugin configuration contains an invalid ID or non-absolute manifest path")
		}
		if seen[item.ID] {
			return config{}, fmt.Errorf("plugin configuration repeats ID %q", item.ID)
		}
		seen[item.ID] = true
	}
	sortEntries(cfg.Plugins)
	return cfg, nil
}

func (s Service) save(cfg config) error {
	home, err := s.ensureHome()
	if err != nil {
		return err
	}
	sortEntries(cfg.Plugins)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, ".plugins-*.tmp")
	if err != nil {
		return fmt.Errorf("create plugin configuration temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
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
		return fmt.Errorf("write plugin configuration: %w", err)
	}
	if err := os.Rename(tmpName, s.configPath()); err != nil {
		return fmt.Errorf("replace plugin configuration: %w", err)
	}
	err = syncPluginsDirectory(home)
	if err != nil {
		return fmt.Errorf("sync plugin configuration directory: %w", err)
	}
	return nil
}

func (s Service) ensureHome() (string, error) {
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
		return "", fmt.Errorf("secure pk home: %w", err)
	}
	return home, nil
}

func (s Service) configPath() string {
	home, err := filepath.Abs(s.Home)
	if err != nil {
		return filepath.Join(s.Home, "plugins.json")
	}
	return filepath.Join(home, "plugins.json")
}

func (s Service) withLock(fn func() error) error {
	home, err := s.ensureHome()
	if err != nil {
		return err
	}
	unlock, err := lockPlugins(filepath.Join(home, "plugins.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func sortEntries(items []entry) {
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
}
