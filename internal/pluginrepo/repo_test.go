package pluginrepo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/plugins"
)

func TestDiscoverThenInstallBuildsMissingGoWorkerWithoutExecutingIt(t *testing.T) {
	repo := t.TempDir()
	writeSourceFile(t, filepath.Join(repo, "go.mod"), "module fixture.example/plugin\n\ngo 1.23.0\n")
	component := filepath.Join(repo, "examples", "plugins", "agentmail")
	if err := os.MkdirAll(component, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, filepath.Join(component, "main.go"), `package main
import "fmt"
func main() { fmt.Println("worker started") }
`)
	manifest := `{"api_version":"pk.extensions/v1","id":"agentmail-readonly","version":"0.1.0","executable":"./agentmail-worker","tools":[{"name":"mail_inboxes","description":"List mail","parameters":{"type":"object","properties":{}}}]}`
	manifestPath := filepath.Join(component, "manifest.json")
	writeSourceFile(t, manifestPath, manifest)
	marker := filepath.Join(repo, "worker-ran")
	writeSourceFile(t, filepath.Join(component, "source.txt"), "worker side effect marker: "+marker)

	catalog, err := Discover(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Candidates) != 1 || catalog.Candidates[0].ID != "agentmail-readonly" || !catalog.Candidates[0].BuildRequired {
		t.Fatalf("catalog=%+v", catalog)
	}
	if _, err := os.Stat(filepath.Join(component, "agentmail-worker")); !os.IsNotExist(err) {
		t.Fatalf("discovery built or created worker: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("discovery executed worker: %v", err)
	}

	home := t.TempDir()
	installed, err := (Installer{Home: home}).Install(context.Background(), repo, catalog.Candidates[0].ManifestPath, catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if installed.ID != "agentmail-readonly" || installed.Revision != catalog.Revision || !strings.HasPrefix(installed.ManifestPath, filepath.Join(home, "plugins", installed.ID)+string(filepath.Separator)) {
		t.Fatalf("installed=%+v", installed)
	}
	worker := filepath.Join(filepath.Dir(installed.ManifestPath), "agentmail-worker")
	info, err := os.Stat(worker)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("built worker stat=%v err=%v", info, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("installer executed worker: %v", err)
	}
	provenanceData, err := os.ReadFile(filepath.Join(home, "plugins", installed.ID, "provenance.json"))
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	var provenance map[string]any
	if err := json.Unmarshal(provenanceData, &provenance); err != nil {
		t.Fatal(err)
	}
	if provenance["revision"] != catalog.Revision || provenance["source"] != catalog.Source {
		t.Fatalf("provenance=%v catalog=%+v", provenance, catalog)
	}
	service := plugins.Service{Home: home}
	if _, err := service.Enable(installed.ManifestPath); err != nil {
		t.Fatalf("enable built managed extension: %v", err)
	}
	if err := service.Remove(installed.ID); err != nil {
		t.Fatalf("remove managed extension: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", installed.ID)); !os.IsNotExist(err) {
		t.Fatalf("managed files remained after remove: %v", err)
	}
}

func TestCatalogReportsUnsupportedAgentFormatsWithoutLoadingWorkers(t *testing.T) {
	repo := t.TempDir()
	writeSourceFile(t, filepath.Join(repo, ".claude-plugin", "plugin.json"), `{"name":"untrusted"}`)
	writeSourceFile(t, filepath.Join(repo, ".pi", "extensions", "entry.ts"), "throw new Error('must not run')")
	catalog, err := Discover(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Candidates) != 0 {
		t.Fatalf("unsupported source exposed candidates: %+v", catalog.Candidates)
	}
	formats := make([]string, 0, len(catalog.Unsupported))
	for _, item := range catalog.Unsupported {
		formats = append(formats, item.Format)
	}
	if !reflect.DeepEqual(formats, []string{"Claude Code plugin", "Pi extension"}) {
		t.Fatalf("unsupported formats=%v", formats)
	}
}

func TestNormalizeRepositoryRejectsNonGitHubAndAmbiguousPaths(t *testing.T) {
	for _, source := range []string{"owner/repo/extra", "http://github.com/owner/repo", "https://github.com/owner/repo/tree/main", "https://user:secret@github.com/owner/repo"} {
		if _, err := normalizeRepository(source); err == nil {
			t.Errorf("accepted invalid source %q", source)
		}
	}
	got, err := normalizeRepository("owner/repo")
	if err != nil || got != "https://github.com/owner/repo.git" {
		t.Fatalf("normalize=%q err=%v", got, err)
	}
}

func TestServiceRemoveIntegrationForgetOnlyExternalManifests(t *testing.T) {
	// This test covers the expected package behavior once Service.Remove lands;
	// external manifests must not be deleted by a plugin ID removal.
	home := t.TempDir()
	external := filepath.Join(t.TempDir(), "manifest.json")
	writeSourceFile(t, external, `{"api_version":"pk.extensions/v1","id":"external","version":"1.0.0","executable":"/bin/echo"}`)
	service := plugins.Service{Home: home}
	if _, err := service.Enable(external); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove("external"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("external manifest was deleted: %v", err)
	}
	items, err := service.List()
	if err != nil || len(items) != 0 {
		t.Fatalf("plugins after remove=%+v err=%v", items, err)
	}
}

func writeSourceFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
