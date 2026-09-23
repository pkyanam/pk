package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkyanam/pk/internal/pluginrepo"
	pluginconfig "github.com/pkyanam/pk/internal/plugins"
)

func discoverRPCPluginSource(ctx context.Context, source string) (map[string]any, error) {
	catalog, err := pluginrepo.Discover(ctx, source)
	if err != nil {
		return nil, err
	}
	return map[string]any{"source": catalog.Source, "revision": catalog.Revision,
		"candidates": catalog.Candidates, "unsupported": catalog.Unsupported}, nil
}

func installRPCPluginSource(ctx context.Context, service pluginconfig.Service, source, manifestPath, revision string) (map[string]any, error) {
	installed, err := (pluginrepo.Installer{Home: service.Home}).Install(ctx, source, manifestPath, revision)
	if err != nil {
		return nil, err
	}
	plugin, err := service.Enable(installed.ManifestPath)
	if err != nil {
		managedDir := filepath.Join(service.Home, "plugins", installed.ID)
		_ = os.RemoveAll(managedDir)
		return nil, fmt.Errorf("plugin was installed but could not be enabled: %w", err)
	}
	updated, err := updatedRPCPlugins(service)
	if err != nil {
		return nil, err
	}
	updated["installed"] = installed
	updated["plugin"] = plugin
	return updated, nil
}

func enableRPCPluginID(service pluginconfig.Service, id string) (map[string]any, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("plugin ID is required")
	}
	items, err := service.List()
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == id {
			if _, err := service.Enable(item.ManifestPath); err != nil {
				return nil, err
			}
			return updatedRPCPlugins(service)
		}
	}
	manifestPath, err := installedPluginManifest(service.Home, id)
	if err != nil {
		return nil, err
	}
	if _, err := service.Enable(manifestPath); err != nil {
		return nil, err
	}
	return updatedRPCPlugins(service)
}

func removeRPCPluginID(service pluginconfig.Service, id string) (map[string]any, error) {
	if err := service.Remove(id); err != nil {
		return nil, err
	}
	return updatedRPCPlugins(service)
}

func installedPluginManifest(home, id string) (string, error) {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, `/\\`) || id == "." || id == ".." {
		return "", errors.New("invalid plugin ID")
	}
	root := filepath.Join(home, "plugins", id)
	data, err := os.ReadFile(filepath.Join(root, "provenance.json"))
	if err != nil {
		return "", fmt.Errorf("plugin %q is not installed: %w", id, err)
	}
	var provenance struct {
		ManifestPath string `json:"manifest_path"`
	}
	if err := json.Unmarshal(data, &provenance); err != nil {
		return "", fmt.Errorf("read plugin installation metadata: %w", err)
	}
	manifestPath := filepath.Join(root, filepath.FromSlash(provenance.ManifestPath))
	rel, err := filepath.Rel(root, manifestPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("plugin installation metadata contains an invalid manifest path")
	}
	return manifestPath, nil
}

func runPluginCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pk plugin discover SOURCE | add [--id ID] SOURCE | list | enable ID | disable ID | remove ID")
		return 2
	}
	service := userPluginService()
	switch args[0] {
	case "discover", "add":
		flags := flag.NewFlagSet("plugin "+args[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		id := flags.String("id", "", "candidate extension ID to install and enable")
		if err := flags.Parse(args[1:]); err != nil {
			return 2
		}
		if flags.NArg() != 1 {
			fmt.Fprintf(stderr, "usage: pk plugin %s [--id ID] SOURCE\n", args[0])
			return 2
		}
		source := flags.Arg(0)
		catalog, err := pluginrepo.Discover(ctx, source)
		if err != nil {
			fmt.Fprintf(stderr, "pk plugin: %v\n", err)
			return 1
		}
		payload := map[string]any{"source": catalog.Source, "revision": catalog.Revision, "candidates": catalog.Candidates, "unsupported": catalog.Unsupported}
		if args[0] == "discover" || strings.TrimSpace(*id) == "" {
			return writePluginJSON(stdout, payload)
		}
		var selected string
		for _, candidate := range catalog.Candidates {
			if candidate.ID == strings.TrimSpace(*id) {
				if selected != "" {
					fmt.Fprintf(stderr, "pk plugin: source contains duplicate ID %q; select by manifest path through the UI\n", *id)
					return 1
				}
				selected = candidate.ManifestPath
			}
		}
		if selected == "" {
			fmt.Fprintf(stderr, "pk plugin: candidate %q was not found; run `pk plugin discover %s` to inspect supported components\n", *id, source)
			return 1
		}
		installed, err := installRPCPluginSource(ctx, service, source, selected, catalog.Revision)
		if err != nil {
			fmt.Fprintf(stderr, "pk plugin: %v\n", err)
			return 1
		}
		return writePluginJSON(stdout, installed)
	case "list":
		items, err := service.List()
		if err != nil {
			fmt.Fprintf(stderr, "pk plugin: %v\n", err)
			return 1
		}
		return writePluginJSON(stdout, map[string]any{"plugins": items})
	case "enable":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: pk plugin enable ID")
			return 2
		}
		payload, err := enableRPCPluginID(service, args[1])
		if err != nil {
			fmt.Fprintf(stderr, "pk plugin: %v\n", err)
			return 1
		}
		return writePluginJSON(stdout, payload)
	case "disable":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: pk plugin disable ID")
			return 2
		}
		if err := service.Disable(args[1]); err != nil {
			fmt.Fprintf(stderr, "pk plugin: %v\n", err)
			return 1
		}
		payload, err := updatedRPCPlugins(service)
		if err != nil {
			fmt.Fprintf(stderr, "pk plugin: %v\n", err)
			return 1
		}
		return writePluginJSON(stdout, payload)
	case "remove":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: pk plugin remove ID")
			return 2
		}
		payload, err := removeRPCPluginID(service, args[1])
		if err != nil {
			fmt.Fprintf(stderr, "pk plugin: %v\n", err)
			return 1
		}
		return writePluginJSON(stdout, payload)
	default:
		fmt.Fprintf(stderr, "unknown plugin command %q\n", args[0])
		return 2
	}
}

func writePluginJSON(writer io.Writer, value any) int {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "pk plugin: write result: %v\n", err)
		return 1
	}
	return 0
}
