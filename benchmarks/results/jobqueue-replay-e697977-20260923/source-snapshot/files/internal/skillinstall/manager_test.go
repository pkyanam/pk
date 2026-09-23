package skillinstall

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: nil}
}

func TestDiscoverAndInstallCopyDataWithoutExecutingScripts(t *testing.T) {
	root := t.TempDir()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "api.github.com" && strings.HasSuffix(r.URL.Path, "/repo"):
			return response(200, `{"default_branch":"main"}`), nil
		case r.URL.Host == "api.github.com" && strings.Contains(r.URL.Path, "/git/trees/"):
			return response(200, `{"tree":[{"path":"skills/review/SKILL.md","type":"blob","mode":"100644"},{"path":"skills/review/example.txt","type":"blob","mode":"100644"},{"path":"skills/review/install.sh","type":"blob","mode":"100755"},{"path":"skills/review/link","type":"blob","mode":"120000"},{"path":"skills/review/../../escape","type":"blob","mode":"100644"}]}`), nil
		case r.URL.Host == "raw.githubusercontent.com" && strings.HasSuffix(r.URL.Path, "SKILL.md"):
			return response(200, "---\nname: review\ndescription: Review code safely.\n---\nInstructions."), nil
		case r.URL.Host == "raw.githubusercontent.com" && strings.HasSuffix(r.URL.Path, "example.txt"):
			return response(200, "example"), nil
		case r.URL.Host == "raw.githubusercontent.com" && strings.HasSuffix(r.URL.Path, "install.sh"):
			return response(200, "echo must never run"), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})}
	m := Manager{Root: filepath.Join(root, "skills"), Client: client}
	candidates, err := m.Discover(context.Background(), "owner/repo")
	if err != nil || len(candidates) != 1 || candidates[0].Name != "review" {
		t.Fatalf("Discover() = %#v, %v", candidates, err)
	}
	installed, err := m.Install(context.Background(), "owner/repo", candidates[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Source != "owner/repo@main" {
		t.Fatalf("source = %q", installed.Source)
	}
	info, err := os.Stat(filepath.Join(installed.Path, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		t.Fatalf("skill script executable: %v", info.Mode())
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Fatalf("traversal target exists or stat failed unexpectedly: %v", err)
	}
	listed, err := m.List()
	if err != nil || len(listed) != 1 || listed[0].Name != "review" {
		t.Fatalf("List() = %#v, %v", listed, err)
	}
	if _, err := m.Install(context.Background(), "owner/repo", candidates[0].Path); err == nil {
		t.Fatal("Install overwrote existing skill")
	}
	if err := m.Remove("review"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(installed.Path); !os.IsNotExist(err) {
		t.Fatalf("Remove left destination: %v", err)
	}
}

func TestInstallRejectsUntrackedRemovalAndUnsafeSource(t *testing.T) {
	root := t.TempDir()
	m := Manager{Root: filepath.Join(root, "skills")}
	if err := os.MkdirAll(filepath.Join(m.Root, "local"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("local"); err == nil {
		t.Fatal("removed an unmanaged skill")
	}
	for _, source := range []string{"../repo", "owner/repo/extra", "http://github.com/a/b", "https://evil.example/a/b"} {
		if _, err := parseSource(source); err == nil {
			t.Errorf("parseSource(%q) succeeded", source)
		}
	}
	if _, err := m.Search(context.Background(), "a"); err == nil {
		t.Fatal("accepted undersized search query")
	}
}

func TestSearchUsesUpstreamCLIEndpointAndBoundsResults(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "skills.sh" || r.URL.Path != "/api/search" || r.URL.Query().Get("q") != "coding tools" || r.URL.Query().Get("limit") != "20" {
			t.Fatalf("unexpected request: %s", r.URL)
		}
		return response(200, `{"skills":[{"id":"owner/repo/skill","name":"skill","installs":17,"source":"owner/repo"}]}`), nil
	})}
	got, err := (Manager{Client: client}).Search(context.Background(), " coding tools ")
	if err != nil || len(got) != 1 || got[0].URL != "https://skills.sh/owner/repo/skill" {
		t.Fatalf("Search() = %#v, %v", got, err)
	}
}

func TestCrossHostRedirectIsRefusedWithoutMutatingSharedClient(t *testing.T) {
	shared := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "skills.sh" {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://attacker.example/collect"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		t.Fatal("cross-host redirect was followed")
		return nil, nil
	})}
	_, err := (Manager{Client: shared}).Search(context.Background(), "safe query")
	if err == nil {
		t.Fatal("Search accepted cross-host redirect")
	}
	if shared.CheckRedirect != nil {
		t.Fatal("manager mutated caller's shared client")
	}
}

func TestManagedRootSymlinkIsRejected(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("symlink test uses Unix semantics")
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "managed")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (Manager{Root: link}).List(); err == nil {
		t.Fatal("accepted symlink managed root")
	}
}
