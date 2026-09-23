package plugins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/pkyanam/pk/internal/extensions"
)

func testManifest(id, toolName string) extensions.Manifest {
	executable, _ := os.Executable()
	return extensions.Manifest{
		APIVersion: extensions.ProtocolVersion,
		ID:         id,
		Version:    "1.0.0",
		Executable: executable,
		Tools:      []extensions.ToolSpec{{Name: toolName, Description: "test tool", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}},
	}
}

func TestEnableRejectsUnavailableWorkerWithoutPersisting(t *testing.T) {
	root := t.TempDir()
	manifestDir := t.TempDir()
	worker := filepath.Join(manifestDir, "worker")
	manifest := testManifest("missing-worker", "missing_tool")
	manifest.Executable = worker
	path := writeManifest(t, manifestDir, manifest)
	service := Service{Home: root}
	if _, err := service.Enable(path); err == nil || !strings.Contains(err.Error(), "worker is unavailable") {
		t.Fatalf("Enable missing worker error=%v", err)
	}
	plugins, err := service.List()
	if err != nil || len(plugins) != 0 {
		t.Fatalf("failed enable persisted config: plugins=%+v err=%v", plugins, err)
	}

	if runtime.GOOS == "windows" {
		t.Skip("Windows executable mode bits are not meaningful")
	}
	if err := os.WriteFile(worker, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(path); err == nil || !strings.Contains(err.Error(), "is not executable") {
		t.Fatalf("Enable non-executable worker error=%v", err)
	}
}

func TestListAndEnabledManifestsMarkWorkerRemovedAfterEnable(t *testing.T) {
	root := t.TempDir()
	manifestDir := t.TempDir()
	worker := filepath.Join(manifestDir, "worker")
	if err := os.WriteFile(worker, []byte("worker placeholder"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := testManifest("vanishing-worker", "vanishing_tool")
	manifest.Executable = worker
	path := writeManifest(t, manifestDir, manifest)
	service := Service{Home: root}
	if _, err := service.Enable(path); err != nil {
		t.Fatalf("Enable valid worker: %v", err)
	}
	if err := os.Remove(worker); err != nil {
		t.Fatal(err)
	}
	plugins, err := service.List()
	if err != nil || len(plugins) != 1 || !strings.Contains(plugins[0].Error, "worker unavailable") {
		t.Fatalf("List unavailable worker=%+v err=%v", plugins, err)
	}
	manifests, issues, err := service.EnabledManifests()
	if err != nil || len(manifests) != 0 || len(issues) != 1 || !strings.Contains(issues[0].Error(), "worker unavailable") {
		t.Fatalf("EnabledManifests=%+v issues=%v err=%v", manifests, issues, err)
	}
}

func writeManifest(t *testing.T, dir string, m extensions.Manifest) string {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, m.ID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEnableListDisablePersistsPrivatelyAndReturnsOnlyExplicitManifests(t *testing.T) {
	home, manifestsDir := filepath.Join(t.TempDir(), "pk-home"), t.TempDir()
	path := writeManifest(t, manifestsDir, testManifest("alpha", "alpha_tool"))
	service := Service{Home: home}
	if plugins, err := service.List(); err != nil || len(plugins) != 0 {
		t.Fatalf("initial list=%+v err=%v", plugins, err)
	}
	enabled, err := service.Enable(path)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.ID != "alpha" || !filepath.IsAbs(enabled.ManifestPath) {
		t.Fatalf("enabled result: %+v", enabled)
	}
	info, err := os.Stat(filepath.Join(home, "plugins.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions %o, want 600", info.Mode().Perm())
	}
	if homeInfo, err := os.Stat(home); err != nil || homeInfo.Mode().Perm() != 0o700 {
		t.Fatalf("home permissions %v, %v", homeInfo, err)
	}
	loaded, issues, err := service.EnabledManifests()
	if err != nil || len(issues) != 0 || len(loaded) != 1 || loaded[0].ID != "alpha" {
		t.Fatalf("enabled manifests=%+v issues=%v err=%v", loaded, issues, err)
	}
	if err := service.Disable("alpha"); err != nil {
		t.Fatal(err)
	}
	listed, err := service.List()
	if err != nil || len(listed) != 1 || listed[0].Enabled {
		t.Fatalf("disabled plugin list=%+v err=%v", listed, err)
	}
	loaded, issues, err = service.EnabledManifests()
	if err != nil || len(issues) != 0 || len(loaded) != 0 {
		t.Fatalf("disabled plugin was still loaded: %+v %v %v", loaded, issues, err)
	}
}

func TestEnableRejectsManifestAndRegistrationConflicts(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	service := Service{Home: home}
	firstPath := writeManifest(t, dir, testManifest("first", "shared_tool"))
	if _, err := service.Enable(firstPath); err != nil {
		t.Fatal(err)
	}
	secondPath := writeManifest(t, dir, testManifest("second", "shared_tool"))
	if _, err := service.Enable(secondPath); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("duplicate tool registration error=%v", err)
	}
	plugins, err := service.List()
	if err != nil || len(plugins) != 1 || plugins[0].ID != "first" {
		t.Fatalf("failed enable mutated config: %+v %v", plugins, err)
	}
	if _, err := service.Enable(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing manifest enabled")
	}
}

func TestNamespacedCommandsAllowSharedLeafNames(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	service := Service{Home: home}
	first := testManifest("alpha", "alpha_tool")
	first.Commands = []extensions.CommandSpec{{Name: "status", Description: "Show alpha status"}}
	second := testManifest("beta", "beta_tool")
	second.Commands = []extensions.CommandSpec{{Name: "status", Description: "Show beta status"}}
	firstPath := writeManifest(t, dir, first)
	secondPath := writeManifest(t, dir, second)
	if _, err := service.Enable(firstPath); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(secondPath); err != nil {
		t.Fatalf("same command leaf with distinct namespaces rejected: %v", err)
	}
	plugins, err := service.List()
	if err != nil || len(plugins) != 2 || plugins[0].Error != "" || plugins[1].Error != "" {
		t.Fatalf("plugin list=%+v err=%v", plugins, err)
	}
	manifests, issues, err := service.EnabledManifests()
	if err != nil || len(issues) != 0 || len(manifests) != 2 {
		t.Fatalf("enabled manifests=%+v issues=%v err=%v", manifests, issues, err)
	}
}

func TestEnabledManifestsAreSortedAndSkipMissingOrChangedEntries(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	service := Service{Home: home}
	second := writeManifest(t, dir, testManifest("zeta", "zeta_tool"))
	first := writeManifest(t, dir, testManifest("alpha", "alpha_tool"))
	if _, err := service.Enable(second); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(first); err != nil {
		t.Fatal(err)
	}
	loaded, issues, err := service.EnabledManifests()
	if err != nil || len(issues) != 0 || len(loaded) != 2 || loaded[0].ID != "alpha" || loaded[1].ID != "zeta" {
		t.Fatalf("manifest order=%+v issues=%v err=%v", loaded, issues, err)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	loaded, issues, err = service.EnabledManifests()
	if err != nil || len(loaded) != 1 || loaded[0].ID != "alpha" || len(issues) != 1 {
		t.Fatalf("missing manifest handling=%+v issues=%v err=%v", loaded, issues, err)
	}
}

func TestConcurrentEnableDoesNotLoseEntries(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	service := Service{Home: home}
	const count = 8
	paths := make([]string, 0, count)
	for i := 0; i < count; i++ {
		id := []string{"a", "b", "c", "d", "e", "f", "g", "h"}[i]
		paths = append(paths, writeManifest(t, dir, testManifest(id, id+"_tool")))
	}
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for _, path := range paths {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			_, err := service.Enable(path)
			errs <- err
		}(path)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	plugins, err := service.List()
	if err != nil || len(plugins) != count {
		t.Fatalf("concurrent enables produced %d entries, err=%v", len(plugins), err)
	}
}

func TestLoadRejectsPrivateConfigCorruption(t *testing.T) {
	home := t.TempDir()
	service := Service{Home: home}
	if err := os.WriteFile(filepath.Join(home, "plugins.json"), []byte(`{"version":99,"plugins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported config version error=%v", err)
	}
}

func TestRemoveForgetsExternalManifestWithoutDeletingIt(t *testing.T) {
	home := t.TempDir()
	external := filepath.Join(t.TempDir(), "manifest.json")
	writePluginManifestForTest(t, external, "external")
	service := Service{Home: home}
	if _, err := service.Enable(external); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove("external"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("external manifest was deleted: %v", err)
	}
	plugins, err := service.List()
	if err != nil || len(plugins) != 0 {
		t.Fatalf("plugins after removal=%+v err=%v", plugins, err)
	}
}

func TestRemoveDeletesOnlyManagedPluginFolder(t *testing.T) {
	home := t.TempDir()
	pluginDir := filepath.Join(home, "plugins", "managed")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(pluginDir, "manifest.json")
	writePluginManifestForTest(t, manifest, "managed")
	service := Service{Home: home}
	if _, err := service.Enable(manifest); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove("managed"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pluginDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed plugin directory still exists: %v", err)
	}
}

func TestRemoveRefusesSymlinkedManagedRoot(t *testing.T) {
	home, externalRoot := t.TempDir(), t.TempDir()
	if err := os.Symlink(externalRoot, filepath.Join(home, "plugins")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	pluginDir := filepath.Join(externalRoot, "managed")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(pluginDir, "manifest.json")
	writePluginManifestForTest(t, manifest, "managed")
	service := Service{Home: home}
	if _, err := service.Enable(manifest); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove("managed"); err == nil {
		t.Fatal("remove accepted symlinked managed root")
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("external plugin was deleted: %v", err)
	}
}

func writePluginManifestForTest(t *testing.T, path, id string) {
	t.Helper()
	manifest := testManifest(id, "tool_"+id)
	manifest.Executable = "/bin/echo"
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
