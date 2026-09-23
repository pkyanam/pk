package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

type oauthSession struct {
	Config oauth2.Config `json:"config"`
	Token  oauth2.Token  `json:"token"`
}

func oauthSessionFrom(raw string) (*oauthSession, error) {
	if raw == "" {
		return nil, nil
	}
	var session oauthSession
	if err := json.Unmarshal([]byte(raw), &session); err != nil || session.Config.ClientID == "" || session.Config.Endpoint.TokenURL == "" || session.Token.AccessToken == "" {
		return nil, errors.New("stored OAuth session is invalid; run pk mcp login again")
	}
	return &session, nil
}

func newOAuthHandler(config ServerConfig) (auth.OAuthHandler, net.Listener, *http.Client, error) {
	oauthClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var listener net.Listener
	redirectURL := "http://127.0.0.1:37891/oauth/callback"
	var fetcher auth.AuthorizationCodeFetcher
	if config.OAuthLogin {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			oauthClient.CloseIdleConnections()
			return nil, nil, nil, fmt.Errorf("start OAuth callback listener: %w", err)
		}
		listener = ln
		redirectURL = "http://" + ln.Addr().String() + "/oauth/callback"
		fetcher = oauthCallbackFetcher(ln, openAuthURL)
		if config.OAuthFetcher != nil {
			fetcher = config.OAuthFetcher
		}
	} else {
		fetcher = func(context.Context, *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			return nil, errors.New("OAuth session needs authorization; run `pk mcp login <server-id>`")
		}
	}
	newTokenSource := func(ctx context.Context, cfg *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
		return &persistingTokenSource{source: cfg.TokenSource(ctx, token), config: *cfg, save: func(updated oauthSession) error { return saveOAuthSession(config, updated) }}, nil
	}
	var initial oauth2.TokenSource
	if config.Auth.SecretValue != "" {
		stored, err := oauthSessionFrom(config.Auth.SecretValue)
		if err != nil {
			if listener != nil {
				_ = listener.Close()
			}
			oauthClient.CloseIdleConnections()
			return nil, nil, nil, err
		}
		tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, oauthClient)
		initial = &persistingTokenSource{source: stored.Config.TokenSource(tokenCtx, &stored.Token), config: stored.Config, token: &stored.Token, save: func(updated oauthSession) error { return saveOAuthSession(config, updated) }}
		if !config.OAuthLogin && !stored.Token.Valid() { /* TokenSource can refresh; leave it available to the SDK. */
		}
	}
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		RedirectURL:                     redirectURL,
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{redirectURL}, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, ClientName: "pk local coding agent", ApplicationType: "native"}},
		AuthorizationCodeFetcher:        fetcher,
		RequestRefreshToken:             true,
		NewTokenSource:                  newTokenSource,
		InitialTokenSource:              initial,
		Client:                          oauthClient,
	})
	if err != nil {
		if listener != nil {
			_ = listener.Close()
		}
		oauthClient.CloseIdleConnections()
		return nil, nil, nil, fmt.Errorf("configure OAuth handler: %w", err)
	}
	return handler, listener, oauthClient, nil
}

func saveOAuthSession(config ServerConfig, session oauthSession) error {
	store := ConfigStore{Home: config.SecretStoreHome}
	data, err := json.Marshal(session)
	if err != nil {
		return errors.New("encode OAuth session")
	}
	return store.SetOAuthSession(config.Auth.SecretRef, data)
}

type persistingTokenSource struct {
	mu     sync.Mutex
	source oauth2.TokenSource
	config oauth2.Config
	token  *oauth2.Token
	save   func(oauthSession) error
}

func (s *persistingTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	if s.token == nil || token.AccessToken != s.token.AccessToken || token.RefreshToken != s.token.RefreshToken || !token.Expiry.Equal(s.token.Expiry) {
		if err := s.save(oauthSession{Config: s.config, Token: *token}); err != nil {
			return nil, fmt.Errorf("persist refreshed OAuth session")
		}
		s.token = token
	}
	return token, nil
}

func oauthCallbackFetcher(listener net.Listener, open func(context.Context, string) error) auth.AuthorizationCodeFetcher {
	return func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		if args == nil || args.URL == "" {
			return nil, errors.New("OAuth authorization URL is missing")
		}
		parsed, err := url.Parse(args.URL)
		if err != nil || parsed.Scheme != "https" {
			return nil, errors.New("OAuth authorization URL is invalid")
		}
		resultCh := make(chan *auth.AuthorizationResult, 1)
		server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			remote, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(remote)
			if r.Method != "GET" || r.URL.Path != "/oauth/callback" || ip == nil || !ip.IsLoopback() || r.Host != listener.Addr().String() {
				http.Error(w, "Invalid OAuth callback", http.StatusBadRequest)
				return
			}
			q := r.URL.Query()
			code, state := q.Get("code"), q.Get("state")
			if code == "" || state == "" {
				http.Error(w, "OAuth response is missing required fields", http.StatusBadRequest)
				return
			}
			select {
			case resultCh <- &auth.AuthorizationResult{Code: code, State: state, Iss: q.Get("iss")}:
			default:
				http.Error(w, "OAuth callback already received", http.StatusConflict)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "Authorization received. You may return to pk.\n")
		})}
		go func() { _ = server.Serve(listener) }()
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = server.Shutdown(closeCtx)
			_ = listener.Close()
		}()
		if open != nil {
			if err := open(ctx, args.URL); err != nil {
				fmt.Fprintf(os.Stderr, "Open this URL to authorize pk: %s\n", args.URL)
			}
		}
		select {
		case result := <-resultCh:
			return result, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func openAuthURL(ctx context.Context, raw string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
		args = []string{raw}
	case "windows":
		command = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", raw}
	default:
		command = "xdg-open"
		args = []string{raw}
	}
	cmd := exec.CommandContext(ctx, command, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Login explicitly starts one configured OAuth server and waits for the
// browser authorization callback. No account action occurs until called.
func Login(ctx context.Context, store ConfigStore, id string) error {
	servers, err := store.List()
	if err != nil {
		return err
	}
	var selected *ServerConfig
	for i := range servers {
		if servers[i].ID == id {
			selected = &servers[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("MCP server %q is not configured", id)
	}
	if selected.Auth.Mode != "oauth" {
		return fmt.Errorf("MCP server %q is not configured for OAuth", id)
	}
	selected.OAuthLogin = true
	host, report, err := NewHost(ctx, []ServerConfig{*selected})
	if err != nil {
		return err
	}
	defer host.Close()
	for _, loaded := range report.Loaded {
		if loaded == id {
			return nil
		}
	}
	if len(report.Warnings) > 0 {
		return errors.New("MCP OAuth login failed; run `pk mcp status` for the server and retry")
	}
	return errors.New("MCP OAuth login did not complete")
}
