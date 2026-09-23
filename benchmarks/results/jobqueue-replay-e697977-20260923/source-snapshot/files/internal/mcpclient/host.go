package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	clientName    = "pk"
	clientVersion = "0.1.0"
)

type Tool struct {
	ServerID       string         `json:"server_id"`
	ServerToolName string         `json:"server_tool_name"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	InputSchema    map[string]any `json:"input_schema"`
	session        *mcp.ClientSession
}

type Report struct {
	Loaded   []string `json:"loaded"`
	Warnings []string `json:"warnings"`
}

type Host struct {
	mu      sync.RWMutex
	servers map[string]*server
	tools   map[string]Tool
	ordered []Tool
	closed  bool
}

type server struct {
	config      ServerConfig
	session     *mcp.ClientSession
	httpClient  *http.Client
	oauthClient *http.Client
}

// NewHost starts only the supplied stdio servers. A failing server is isolated
// and reported; successfully initialized servers remain usable.
func NewHost(ctx context.Context, configs []ServerConfig) (*Host, Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(configs) > maxServers {
		return nil, Report{}, fmt.Errorf("at most %d MCP servers may be configured", maxServers)
	}
	host := &Host{servers: make(map[string]*server), tools: make(map[string]Tool)}
	report := Report{Loaded: []string{}, Warnings: []string{}}
	seenIDs := make(map[string]bool)
	for _, config := range configs {
		if err := ctx.Err(); err != nil {
			_ = host.Close()
			return nil, report, err
		}
		if err := config.validate(); err != nil {
			report.Warnings = append(report.Warnings, err.Error())
			continue
		}
		if seenIDs[config.ID] {
			report.Warnings = append(report.Warnings, fmt.Sprintf("duplicate MCP server id %q; ignored", config.ID))
			continue
		}
		seenIDs[config.ID] = true
		loaded, tools, err := connectServer(ctx, config)
		if err != nil {
			message := err.Error()
			if config.URL != "" && config.Auth.Mode != "" && config.Auth.Mode != "none" {
				message = "authenticated remote connection failed; details suppressed"
			}
			report.Warnings = append(report.Warnings, fmt.Sprintf("MCP server %q: %s", config.ID, message))
			continue
		}
		host.servers[config.ID] = loaded
		report.Loaded = append(report.Loaded, config.ID)
		for _, tool := range tools {
			if _, exists := host.tools[tool.Name]; exists {
				report.Warnings = append(report.Warnings, fmt.Sprintf("MCP tool export name %q collides; ignored", tool.Name))
				continue
			}
			tool.session = loaded.session
			host.tools[tool.Name] = tool
			host.ordered = append(host.ordered, tool)
		}
	}
	sort.Slice(host.ordered, func(i, j int) bool { return host.ordered[i].Name < host.ordered[j].Name })
	sort.Strings(report.Loaded)
	sort.Strings(report.Warnings)
	return host, report, nil
}

func connectServer(ctx context.Context, config ServerConfig) (*server, []Tool, error) {
	if config.URL != "" {
		return connectHTTPServer(ctx, config)
	}
	cmd := exec.Command(config.Command, config.Args...)
	if config.WorkingDirectory != "" {
		cmd.Dir = filepath.Clean(config.WorkingDirectory)
	} else if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cmd.Env = processEnvironment(config.Env)
	transport := &mcp.CommandTransport{Command: cmd, TerminateDuration: 1200 * time.Millisecond}
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: clientVersion}, nil)
	connectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = session.Close()
		}
	}()
	tools := make([]Tool, 0)
	for remoteTool, err := range session.Tools(connectCtx, nil) {
		if err != nil {
			return nil, nil, fmt.Errorf("list tools: %w", err)
		}
		if len(tools) >= maxToolsPerServer {
			return nil, nil, fmt.Errorf("tool count exceeds %d", maxToolsPerServer)
		}
		converted, err := convertTool(config.ID, remoteTool)
		if err != nil {
			return nil, nil, err
		}
		tools = append(tools, converted)
	}
	closeOnError = false
	return &server{config: config, session: session}, tools, nil
}

func connectHTTPServer(ctx context.Context, config ServerConfig) (*server, []Tool, error) {
	if err := config.validate(); err != nil {
		return nil, nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Transport: authRoundTripper{base: transport, config: config},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	mcpTransport := &mcp.StreamableClientTransport{Endpoint: config.URL, HTTPClient: client, DisableStandaloneSSE: true}
	clientMCP := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: clientVersion}, nil)
	var oauthHTTPClient *http.Client
	connectTimeout := 20 * time.Second
	if config.OAuthLogin {
		connectTimeout = 5 * time.Minute
	}
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if config.Auth.Mode == "oauth" {
		oauthHandler, listener, authClient, err := newOAuthHandler(config)
		if err != nil {
			client.CloseIdleConnections()
			return nil, nil, err
		}
		if listener != nil {
			defer listener.Close()
		}
		oauthHTTPClient = authClient
		mcpTransport.OAuthHandler = oauthHandler
	}
	session, err := clientMCP.Connect(connectCtx, mcpTransport, nil)
	if err != nil {
		client.CloseIdleConnections()
		if oauthHTTPClient != nil {
			oauthHTTPClient.CloseIdleConnections()
		}
		return nil, nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = session.Close()
			client.CloseIdleConnections()
			if oauthHTTPClient != nil {
				oauthHTTPClient.CloseIdleConnections()
			}
		}
	}()
	tools := make([]Tool, 0)
	for remoteTool, err := range session.Tools(connectCtx, nil) {
		if err != nil {
			return nil, nil, fmt.Errorf("list tools: %w", err)
		}
		if len(tools) >= maxToolsPerServer {
			return nil, nil, fmt.Errorf("tool count exceeds %d", maxToolsPerServer)
		}
		converted, err := convertTool(config.ID, remoteTool)
		if err != nil {
			return nil, nil, err
		}
		tools = append(tools, converted)
	}
	closeOnError = false
	return &server{config: config, session: session, httpClient: client, oauthClient: oauthHTTPClient}, tools, nil
}

type authRoundTripper struct {
	base   *http.Transport
	config ServerConfig
}

func (t authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.config.Auth.Mode == "bearer_env" {
		token := strings.TrimSpace(os.Getenv(t.config.Auth.BearerEnv))
		if token == "" {
			return nil, fmt.Errorf("MCP bearer credential environment variable %s is empty", t.config.Auth.BearerEnv)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	} else if t.config.Auth.Mode == "bearer_secret" {
		if t.config.Auth.SecretValue == "" {
			return nil, errors.New("MCP bearer credential is unavailable")
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(t.config.Auth.SecretValue))
	} else if t.config.Auth.Mode == "header_env" {
		value := os.Getenv(t.config.Auth.HeaderValueEnv)
		if value == "" {
			return nil, fmt.Errorf("MCP header credential environment variable %s is empty", t.config.Auth.HeaderValueEnv)
		}
		req.Header.Set(t.config.Auth.HeaderName, value)
	} else if t.config.Auth.Mode == "header_secret" {
		if t.config.Auth.SecretValue == "" {
			return nil, errors.New("MCP header credential is unavailable")
		}
		req.Header.Set(t.config.Auth.HeaderName, t.config.Auth.SecretValue)
	}
	return t.base.RoundTrip(req)
}
func (t authRoundTripper) CloseIdleConnections() { t.base.CloseIdleConnections() }

func convertTool(serverID string, remote *mcp.Tool) (Tool, error) {
	if remote == nil || strings.TrimSpace(remote.Name) == "" {
		return Tool{}, errors.New("server returned a tool without a name")
	}
	encoded, err := json.Marshal(remote.InputSchema)
	if err != nil || len(encoded) > maxSchemaBytes {
		return Tool{}, fmt.Errorf("tool %q has an invalid or oversized input schema", remote.Name)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil || schema == nil || schema["type"] != "object" {
		return Tool{}, fmt.Errorf("tool %q input schema must be a JSON object schema", remote.Name)
	}
	description := remote.Description
	if len(description) > maxDescription {
		description = description[:maxDescription] + " [description truncated]"
	}
	return Tool{ServerID: serverID, ServerToolName: remote.Name, Name: exportName(serverID, remote.Name), Description: description, InputSchema: schema}, nil
}

// Tools returns a sorted copy of the discovered tool catalog.
func (h *Host) Tools() []Tool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]Tool(nil), h.ordered...)
}

func (h *Host) tool(name string) (Tool, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return Tool{}, false
	}
	tool, ok := h.tools[name]
	return tool, ok
}

func (h *Host) call(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	tool, ok := h.tool(name)
	if !ok {
		return nil, fmt.Errorf("MCP tool %q is unavailable", name)
	}
	if tool.session == nil {
		return nil, errors.New("MCP session is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return tool.session.CallTool(ctx, &mcp.CallToolParams{Name: tool.ServerToolName, Arguments: args})
}

// Close shuts down all connected servers. It is safe to call more than once.
func (h *Host) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	servers := make([]*server, 0, len(h.servers))
	for _, srv := range h.servers {
		servers = append(servers, srv)
	}
	h.mu.Unlock()
	sort.Slice(servers, func(i, j int) bool { return servers[i].config.ID < servers[j].config.ID })
	var first error
	for _, srv := range servers {
		if err := srv.session.Close(); err != nil && first == nil {
			first = fmt.Errorf("close MCP server %q: %w", srv.config.ID, err)
		}
		if srv.httpClient != nil {
			srv.httpClient.CloseIdleConnections()
		}
		if srv.oauthClient != nil {
			srv.oauthClient.CloseIdleConnections()
		}
	}
	return first
}
