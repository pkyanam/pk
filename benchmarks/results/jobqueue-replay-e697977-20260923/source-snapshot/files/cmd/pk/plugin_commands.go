package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/pkyanam/pk/internal/extensions"
)

// pluginSlashCommandCatalog reads only the explicit, session-frozen manifest
// paths supplied by its caller. It never starts a worker or invokes a command.
func pluginSlashCommandCatalog(manifestPaths []string) ([]extensions.SlashCommand, []error) {
	manifests, issues := loadPluginSlashManifests(manifestPaths)
	catalog, catalogIssues := commandCatalogFromManifests(manifests)
	return catalog, append(issues, catalogIssues...)
}

func commandCatalogFromManifests(manifests []extensions.Manifest) ([]extensions.SlashCommand, []error) {
	toolOwners := make(map[string]string)
	ids := make(map[string]bool)
	var catalog []extensions.SlashCommand
	var issues []error
	for _, manifest := range manifests {
		if ids[manifest.ID] {
			issues = append(issues, fmt.Errorf("duplicate plugin ID %q", manifest.ID))
			continue
		}
		conflict := ""
		for _, tool := range manifest.Tools {
			if owner := toolOwners[tool.Name]; owner != "" {
				conflict = fmt.Sprintf("tool %q conflicts with plugin %q", tool.Name, owner)
				break
			}
		}
		if conflict != "" {
			issues = append(issues, fmt.Errorf("plugin %q: %s; plugin omitted from command catalog", manifest.ID, conflict))
			continue
		}
		ids[manifest.ID] = true
		for _, tool := range manifest.Tools {
			toolOwners[tool.Name] = manifest.ID
		}
		for _, command := range manifest.Commands {
			catalog = append(catalog, extensions.SlashCommand{
				Name:        extensions.SlashCommandName(manifest.ID, command.Name),
				ExtensionID: manifest.ID,
				CommandName: command.Name,
				Description: command.Description,
			})
		}
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
	return catalog, issues
}

// executePluginSlashCommand resolves a qualified command against the same
// explicit manifest snapshot used for discovery, then loads only its owner.
func executePluginSlashCommand(ctx context.Context, workspace string, manifestPaths []string, name, arguments string) (string, error) {
	if len(arguments) > extensions.MaxSlashCommandArgumentBytes {
		return "", fmt.Errorf("extension command arguments exceed %d bytes", extensions.MaxSlashCommandArgumentBytes)
	}
	manifests, issues := loadPluginSlashManifests(manifestPaths)
	catalog, catalogIssues := commandCatalogFromManifests(manifests)
	issues = append(issues, catalogIssues...)
	for _, command := range catalog {
		if command.Name != name {
			continue
		}
		for _, manifest := range manifests {
			if manifest.ID != command.ExtensionID {
				continue
			}
			host, report, err := extensions.NewHost(ctx, workspace, []extensions.Manifest{manifest}, nil)
			if err != nil {
				return "", err
			}
			defer host.Close()
			if len(report.Disabled) != 0 || len(report.Loaded) != 1 {
				if len(report.Disabled) != 0 {
					return "", report.Disabled[0]
				}
				return "", errors.New("extension command worker did not load")
			}
			return host.ExecuteSlashCommand(ctx, name, arguments)
		}
	}
	if len(issues) > 0 {
		return "", fmt.Errorf("extension slash command %q is unavailable (%v)", name, issues[0])
	}
	return "", fmt.Errorf("extension slash command %q is unavailable", name)
}

func loadPluginSlashManifests(paths []string) ([]extensions.Manifest, []error) {
	manifests := make([]extensions.Manifest, 0, len(paths))
	var issues []error
	for _, path := range paths {
		manifest, err := extensions.LoadManifest(path)
		if err == nil {
			err = validateSlashWorker(manifest.Executable)
		}
		if err != nil {
			issues = append(issues, fmt.Errorf("plugin manifest %q: %w", path, err))
			continue
		}
		manifests = append(manifests, manifest)
	}
	return manifests, issues
}

func validateSlashWorker(executable string) error {
	resolved := executable
	if !filepath.IsAbs(resolved) {
		found, err := exec.LookPath(resolved)
		if err != nil {
			return fmt.Errorf("worker executable %q is unavailable", resolved)
		}
		resolved = found
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("inspect worker executable: %w", err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return fmt.Errorf("worker executable %q is not a regular executable file", resolved)
	}
	return nil
}
