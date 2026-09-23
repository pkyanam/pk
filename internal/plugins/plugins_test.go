package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pkyanam/pk/internal/extensions"
)

func testManifest(id, toolName string) extensions.Manifest {
	return extensions.Manifest{
		APIVersion: extensions.ProtocolVersion,
		ID:         id,
		Version:    "1.0.0",
		Executable: "/bin/echo",
		Tools:      []extensions.ToolSpec{{Name: toolName, Description: "test tool", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}},
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
