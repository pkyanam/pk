package mcpclient

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConfigStorePersistsExplicitServersPrivately(t *testing.T) {
	home := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := ConfigStore{Home: home}
	if err := store.Add(ServerConfig{ID: "local", Command: executable, Args: []string{"--serve"}, Env: map[string]string{"API_TOKEN": "do-not-print"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("MCP config permissions = %o, want 600", info.Mode().Perm())
	}
	summaries, err := store.Summaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ArgumentsCount != 1 || strings.Join(summaries[0].EnvironmentKeys, ",") != "API_TOKEN" {
		t.Fatalf("summary = %#v", summaries)
	}
	loaded, err := store.List()
	if err != nil || len(loaded) != 1 || loaded[0].Env["API_TOKEN"] != "do-not-print" {
		t.Fatalf("stored config = %#v, %v", loaded, err)
	}
	if err := store.Remove("local"); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.List()
	if err != nil || len(loaded) != 0 {
		t.Fatalf("after remove = %#v, %v", loaded, err)
	}
}

func TestConfigStoreSerializesConcurrentAdds(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := ConfigStore{Home: t.TempDir()}
	const count = 8
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- store.Add(ServerConfig{ID: "server-" + string(rune('a'+i)), Command: executable})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	servers, err := store.List()
	if err != nil || len(servers) != count {
		t.Fatalf("concurrent configured servers = %d, err=%v", len(servers), err)
	}
}

func TestConfigStoreRejectsMalformedConfig(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "mcp.json")
	if err := os.WriteFile(configPath, []byte("{\"version\":1,\"servers\":[],\"unexpected\":true}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (ConfigStore{Home: home}).List(); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("malformed config error = %v", err)
	}
}
