// Package daemon implements the v2 daemon core for the pk2 beta CLI.
//
// The daemon owns all mutable state (single writer), speaks newline-delimited
// JSON-RPC 2.0 over stdio (internal/v2/jsonrpc), and drives real agent turns
// through the provider configuration already stored by v0.1
// (internal/providers.Store, read-only, keys never echoed). Sessions are
// logged in memory and persisted to PK_HOME so a client can reconnect and
// continue the same session.
//
// This package is independent of cmd/pk (v0.1) internals; it only reuses the
// provider store for configuration and the llm types from the unreal-agent
// harness, which v0.1 also uses.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/v2/jsonrpc"
	"github.com/pkyanam/pk/internal/v2/state"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// SessionLogPath is the persisted session transcript location under PK_HOME.
// It stores only the message items (llm.Item JSON), never credentials.
const SessionLogPath = "pk2/sessions"

// Session is the daemon-owned state for one agent conversation.
type Session struct {
	ID       string     `json:"id"`
	Provider string     `json:"provider"`
	Model    string     `json:"model"`
	Items    []jsonItem `json:"items"`
}

// jsonItem is a JSON-serializable llm.Item (llm.Item.Data is any, so we store
// the typed payloads we construct: Message, ToolCall, ToolResult).
type jsonItem struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Daemon holds the daemon's state and its dependencies.
type Daemon struct {
	home     string
	store    *providers.Store
	mu       sync.Mutex
	sessions map[string]*Session

	// providerFactory overrides provider client construction (tests inject a
	// fake llm.Adapter; production leaves it nil to use providers.NewClient).
	providerFactory func() (llm.Adapter, error)
}

// New returns a Daemon rooted at PK_HOME (defaults to ~/.pk when empty).
func New(home string) *Daemon {
	if home == "" {
		if h := os.Getenv("PK_HOME"); h != "" {
			home = h
		} else {
			home = filepath.Join(mustHomeDir(), ".pk")
		}
	}
	return &Daemon{
		home:     home,
		store:    &providers.Store{Home: home},
		sessions: make(map[string]*Session),
	}
}

func mustHomeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}

// Handler returns the JSON-RPC handler map for the daemon.
func (d *Daemon) Handler() map[string]func(jsonrpc.Params) (any, error) {
	return map[string]func(jsonrpc.Params) (any, error){
		"ping": func(jsonrpc.Params) (any, error) {
			return "pong", nil
		},
		"catalog/get":    d.catalogGet,
		"catalog/reload": func(jsonrpc.Params) (any, error) { return d.catalogReload() },
		"session/create": d.sessionCreate,
		"session/attach": d.sessionAttach,
		"prompt":         d.prompt,
		"provider/list":  d.providerList,
	}
}

// Serve runs the JSON-RPC server over r/w until EOF or ctx cancellation.
func (d *Daemon) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	server := &jsonrpc.Server{Handlers: d.Handler()}
	errc := make(chan error, 1)
	go func() { errc <- server.Serve(r, w) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return nil
	}
}

// catalogGet returns the provider catalog State value (the M1 wire form:
// map provider ID -> []Model).
func (d *Daemon) catalogGet(jsonrpc.Params) (any, error) {
	return demoCatalog().Value(), nil
}

// catalogReload re-runs the catalog State rebuild (the hourly refresh path).
func (d *Daemon) catalogReload() (any, error) {
	cat := demoCatalog()
	if err := cat.Rebuild(); err != nil {
		return nil, err
	}
	return cat.Value(), nil
}

// providerList returns configured provider summaries (names only; never keys).
func (d *Daemon) providerList(jsonrpc.Params) (any, error) {
	summaries, err := d.store.Summaries()
	if err != nil {
		return nil, err
	}
	type brief struct {
		ID               string `json:"id"`
		Protocol         string `json:"protocol"`
		BaseURL          string `json:"base_url"`
		DefaultModel     string `json:"default_model,omitempty"`
		APIKeyConfigured bool   `json:"api_key_configured"`
	}
	out := make([]brief, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, brief{ID: s.ID, Protocol: string(s.Protocol), BaseURL: s.BaseURL, DefaultModel: s.DefaultModel, APIKeyConfigured: s.APIKeyConfigured})
	}
	return out, nil
}

// resolveProvider returns the configured provider (default unless overridden)
// without ever exposing its key to the caller. When providerFactory is set
// (tests), it returns a zero provider with a synthetic model so the factory
// path can run without a real configuration.
func (d *Daemon) resolveProvider(providerID string) (providers.Provider, error) {
	if d.providerFactory != nil {
		return providers.Provider{ID: providerID, DefaultModel: "test-model"}, nil
	}
	id := providerID
	if id == "" {
		var err error
		id, err = d.store.DefaultID()
		if err != nil {
			return providers.Provider{}, fmt.Errorf("no default provider configured: %w", err)
		}
	}
	return d.store.Get(id)
}

// saveSession persists the session transcript under PK_HOME.
func (d *Daemon) saveSession(s *Session) error {
	dir := filepath.Join(d.home, SessionLogPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, s.ID+".json"), b, 0o600)
}

// loadSession reads a persisted transcript, if present.
func (d *Daemon) loadSession(id string) (*Session, error) {
	b, err := os.ReadFile(filepath.Join(d.home, SessionLogPath, id+".json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("corrupt session %q: %w", id, err)
	}
	return &s, nil
}

// demoCatalog builds the M1 provider-catalog State with the three example
// transformations. Kept here so the daemon serves the same catalog/get the M1
// tests expect.
func demoCatalog() *state.State[state.Catalog] {
	cat := state.NewCatalog()
	cat.Add(state.TransformCatalog(func() (state.Catalog, error) {
		return state.Catalog{
			"openai":     {{ID: "gpt-6-luna", Provider: "openai", Context: 200_000, Output: 16_384, CostIn: 1.5, CostOut: 7.5}},
			"anthropic":  {{ID: "claude-sonnet-4", Provider: "anthropic", Context: 100_000, Output: 8_192, CostIn: 1.0, CostOut: 5.0}},
			"sphinctral": {{ID: "sphinctral-7b", Provider: "sphinctral", Context: 32_000, Output: 4_096, CostIn: 0.1, CostOut: 0.2}},
		}, nil
	}))
	cat.Add(state.DisableProviders([]string{"sphinctral"}))
	cat.Add(state.HalveLimits())
	return cat
}
