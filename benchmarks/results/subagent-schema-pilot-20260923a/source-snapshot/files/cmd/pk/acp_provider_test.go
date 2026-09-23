package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/runner"
)

func TestACPUsesConfiguredProviderAndProviderDefaults(t *testing.T) {
	for _, test := range []struct {
		name, args, wantModel, wantEffort string
	}{
		{name: "default provider", wantModel: "provider-model", wantEffort: "low"},
		{name: "explicit selection and overrides", args: "--provider fixture --model override-model --effort high", wantModel: "override-model", wantEffort: "high"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			pkHomePath := filepath.Join(home, ".pk")
			t.Setenv("PK_HOME", pkHomePath)
			requestSeen := make(chan map[string]any, 1)
			var requestCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					http.NotFound(w, r)
					return
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode provider request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requestCount.Add(1)
				select {
				case requestSeen <- request:
				default:
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"acp-provider-response\",\"choices\":[{\"delta\":{\"content\":\"provider answer\"},\"finish_reason\":null}]}\n\n")
				_, _ = io.WriteString(w, "data: {\"id\":\"acp-provider-response\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			providerStore := providers.Store{Home: pkHomePath}
			if err := providerStore.Put(providers.Provider{ID: "fixture", Protocol: providers.ProtocolChatCompletions, BaseURL: server.URL, APIKey: "local-fixture-secret", DefaultModel: "provider-model", DefaultEffort: "low", SupportsReasoningEffort: true}); err != nil {
				t.Fatal(err)
			}
			if err := providerStore.SetDefault("fixture"); err != nil {
				t.Fatal(err)
			}
			if err := config.Save(filepath.Join(pkHomePath, "config.json"), config.Config{Model: "config-model", Effort: "medium"}); err != nil {
				t.Fatal(err)
			}

			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			defer outR.Close()
			diagnostics := &acpTestBuffer{}
			exit := make(chan int, 1)
			args := []string{}
			if test.args != "" {
				args = strings.Fields(test.args)
			}
			runCtx, cancelRun := context.WithCancel(context.Background())
			defer cancelRun()
			go func() { exit <- runACPCommand(runCtx, args, inR, outW, diagnostics) }()
			var stopOnce sync.Once
			var exitCode int
			var exited bool
			stopRun := func(cancel bool) {
				stopOnce.Do(func() {
					_ = inW.Close()
					if cancel {
						cancelRun()
						_ = outR.Close()
					}
					select {
					case exitCode = <-exit:
						exited = true
					case <-time.After(3 * time.Second):
						cancelRun()
						_ = outR.Close()
						server.Close()
						select {
						case exitCode = <-exit:
							exited = true
						case <-time.After(3 * time.Second):
						}
					}
					_ = outR.Close()
					server.Close()
				})
			}
			defer stopRun(true)
			scanner := bufio.NewScanner(outR)
			scanner.Buffer(make([]byte, 1024), 1<<20)
			write := func(value any) {
				t.Helper()
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				written := make(chan error, 1)
				go func() {
					_, writeErr := fmt.Fprintln(inW, string(data))
					written <- writeErr
				}()
				select {
				case err := <-written:
					if err != nil {
						stopRun(true)
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					stopRun(true)
					t.Fatal("timed out writing ACP request")
				}
			}
			read := func() map[string]any {
				t.Helper()
				timer := time.AfterFunc(5*time.Second, func() { _ = outR.Close() })
				scanned := scanner.Scan()
				timer.Stop()
				if !scanned {
					stopRun(true)
					if !exited {
						t.Fatal("ACP process did not stop after protocol read timed out")
					}
					t.Fatalf("read ACP response: %v; diagnostics=%s", scanner.Err(), diagnostics.String())
				}
				var value map[string]any
				if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			write(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
			if got := read(); got["id"] != float64(1) {
				t.Fatalf("initialize response=%v", got)
			}
			workspace := filepath.Join(home, "workspace")
			if err := os.Mkdir(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": workspace, "mcpServers": []any{}}})
			created := read()
			sessionID := created["result"].(map[string]any)["sessionId"].(string)
			write(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "answer locally"}}}})
			for {
				message := read()
				if message["id"] == float64(3) {
					if message["result"].(map[string]any)["stopReason"] != "end_turn" {
						t.Fatalf("prompt response=%v", message)
					}
					break
				}
			}
			select {
			case request := <-requestSeen:
				if request["model"] != test.wantModel || request["reasoning_effort"] != test.wantEffort {
					t.Fatalf("provider request model/effort=%v/%v; want %s/%s", request["model"], request["reasoning_effort"], test.wantModel, test.wantEffort)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("local provider received no ACP request")
			}
			if got := requestCount.Load(); got != 1 {
				t.Fatalf("local provider request count=%d, want one", got)
			}
			digest := sha256.Sum256([]byte(sessionID))
			snapshotData, err := os.ReadFile(filepath.Join(pkHomePath, "sessions", hex.EncodeToString(digest[:])+".context.json"))
			var snapshot runner.ContextSnapshot
			if err == nil {
				err = json.Unmarshal(snapshotData, &snapshot)
			}
			if err != nil || snapshot.ProviderID != "fixture" || snapshot.ProviderFingerprint == "" {
				t.Fatalf("saved provider identity=%q fingerprint=%q err=%v", snapshot.ProviderID, snapshot.ProviderFingerprint, err)
			}
			stopRun(false)
			if !exited {
				t.Fatal("ACP did not exit after input closed")
			}
			if strings.Contains(diagnostics.String(), "local-fixture-secret") {
				t.Fatal("ACP diagnostics leaked configured provider key")
			}
			if exitCode != 0 {
				t.Fatalf("ACP exit=%d diagnostics=%s", exitCode, diagnostics.String())
			}
		})
	}
}

type acpTestBuffer struct {
	mu    sync.Mutex
	value strings.Builder
}

func (buffer *acpTestBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.value.Write(data)
}

func (buffer *acpTestBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.value.String()
}
