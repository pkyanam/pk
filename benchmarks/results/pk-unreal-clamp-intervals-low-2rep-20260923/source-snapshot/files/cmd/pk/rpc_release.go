package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type rpcReleaseStatus struct {
	RunningID       string `json:"running_id,omitempty"`
	InstalledID     string `json:"installed_id,omitempty"`
	ReloadAvailable bool   `json:"reload_available"`
}

// currentReleaseID reads only the small release manifest adjacent to a
// managed executable. It deliberately avoids Manager.Status, which scans all
// staged releases and is unnecessary for the running-vs-installed indicator.
func currentReleaseID(directory string) string {
	manifest := filepath.Join(directory, "release.json")
	info, err := os.Stat(manifest)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return ""
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		return ""
	}
	var value struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	if json.Unmarshal(data, &value) != nil || value.ID == "" || filepath.Base(value.ID) != value.ID || strings.HasPrefix(value.ID, ".") || filepath.Base(filepath.Clean(directory)) != value.ID || !filepath.IsAbs(value.Path) || filepath.Base(filepath.Clean(value.Path)) != value.ID {
		return ""
	}
	return value.ID
}

func rpcCurrentReleaseStatus() rpcReleaseStatus {
	return rpcReleaseStatusFor(os.Executable, updateLibraryDir)
}

func rpcReleaseStatusFor(executableFn func() (string, error), libraryDirFn func() string) rpcReleaseStatus {
	var status rpcReleaseStatus
	if executable, err := executableFn(); err == nil {
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			executable = resolved
		}
		for directory, depth := filepath.Dir(executable), 0; depth < 5; directory, depth = filepath.Dir(directory), depth+1 {
			if id := currentReleaseID(directory); id != "" {
				status.RunningID = id
				break
			}
			if filepath.Dir(directory) == directory {
				break
			}
		}
	}
	current := filepath.Join(libraryDirFn(), "current")
	if resolved, err := filepath.EvalSymlinks(current); err == nil {
		status.InstalledID = currentReleaseID(resolved)
	}
	status.ReloadAvailable = status.RunningID != "" && status.InstalledID != "" && status.RunningID != status.InstalledID
	return status
}
