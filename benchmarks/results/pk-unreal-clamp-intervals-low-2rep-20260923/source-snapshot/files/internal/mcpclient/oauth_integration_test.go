package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

func TestOAuthAuthorizationPKCERefreshAndRestart(t *testing.T) {
	var dcrCalls, authorizeCalls, codeGrants, refreshGrants atomic.Int32
	var wantChallenge atomic.Value
	var codeExchangeOK atomic.Bool

	var authURL string
	authMux := http.NewServeMux()
	authMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("unexpected AS %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	})
	authMux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": authURL, "authorization_endpoint": authURL + "/authorize", "token_endpoint": authURL + "/token", "registration_endpoint": authURL + "/register", "jwks_uri": authURL + "/jwks", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none"}, "code_challenge_methods_supported": []string{"S256"}})
	})
	authMux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			RedirectURIs            []string `json:"redirect_uris"`
			TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.RedirectURIs) != 1 || request.TokenEndpointAuthMethod != "none" {
			http.Error(w, "bad dynamic registration", 400)
			return
		}
		redirect, err := url.Parse(request.RedirectURIs[0])
		if err != nil || redirect.Scheme != "http" || !strings.HasPrefix(redirect.Host, "127.0.0.1:") {
			http.Error(w, "unsafe redirect", 400)
			return
		}
		dcrCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"client_id":"pk-test-client","token_endpoint_auth_method":"none"}`)
	})
	authMux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "pk-test-client" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("code_challenge") == "" {
			http.Error(w, "invalid authorization request", 400)
			return
		}
		wantChallenge.Store(q.Get("code_challenge"))
		authorizeCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":"fixture-code","state":"`+q.Get("state")+`"}`)
	})
	authMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad token form", 400)
			return
		}
		var access, refresh string
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			codeGrants.Add(1)
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			actual := base64.RawURLEncoding.EncodeToString(sum[:])
			codeExchangeOK.Store(r.Form.Get("code") == "fixture-code" && actual == wantChallenge.Load().(string))
			access, refresh = "first-access", "refresh-one"
		case "refresh_token":
			refreshGrants.Add(1)
			if r.Form.Get("refresh_token") != "refresh-one" {
				http.Error(w, "invalid refresh", 400)
				return
			}
			access, refresh = "refreshed-access", "refresh-two"
		default:
			http.Error(w, "unsupported grant", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"token_type":"Bearer","expires_in":3600}`, access, refresh)
	})
	AS := httptest.NewServer(authMux)
	defer AS.Close()
	authURL = AS.URL

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "oauth-fixture", Version: "1"}, nil)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "read_doc", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture doc"}}}, nil, nil
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)
	var resourceURL, resourceBase string
	resourceMux := http.NewServeMux()
	resourceMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("unexpected resource %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	})
	resourceMux.Handle("/.well-known/oauth-protected-resource/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": resourceURL, "authorization_servers": []string{authURL}, "scopes_supported": []string{"read"}})
	}))
	resourceMux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer first-access" && r.Header.Get("Authorization") != "Bearer refreshed-access" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resourceBase+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	resource := httptest.NewServer(resourceMux)
	defer resource.Close()
	resourceBase = resource.URL
	resourceURL = resource.URL + "/mcp"
	_, prmErr := oauthex.GetProtectedResourceMetadata(t.Context(), resourceBase+"/.well-known/oauth-protected-resource/mcp", resourceURL, http.DefaultClient)
	if prmErr != nil {
		t.Fatalf("fixture protected resource metadata rejected: %v", prmErr)
	}

	store := ConfigStore{Home: t.TempDir()}
	if err := store.AddOAuth(ServerConfig{ID: "protected", URL: resourceURL}); err != nil {
		t.Fatal(err)
	}
	configs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	configs[0].OAuthLogin = true
	configs[0].OAuthFetcher = func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
		if err != nil {
			return nil, err
		}
		client := AS.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		var result struct {
			Code  string `json:"code"`
			State string `json:"state"`
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			return nil, err
		}
		return &auth.AuthorizationResult{Code: result.Code, State: result.State}, nil
	}
	host, report, err := NewHost(t.Context(), configs)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Loaded) != 1 || len(host.Tools()) != 1 {
		t.Fatalf("login report=%#v tools=%#v", report, host.Tools())
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if !codeExchangeOK.Load() || dcrCalls.Load() != 1 || authorizeCalls.Load() != 1 || codeGrants.Load() != 1 {
		t.Fatalf("OAuth flow DCR=%d authorize=%d code-exchange=%d PKCE-ok=%v", dcrCalls.Load(), authorizeCalls.Load(), codeGrants.Load(), codeExchangeOK.Load())
	}
	if summary, err := store.Summaries(); err != nil || summary[0].AuthStatus != "authenticated" {
		t.Fatalf("post-login summary=%#v err=%v", summary, err)
	}

	configs, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	var saved oauthSession
	if err := json.Unmarshal([]byte(configs[0].Auth.SecretValue), &saved); err != nil {
		t.Fatal(err)
	}
	saved.Token.Expiry = time.Now().Add(-time.Minute)
	data, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetOAuthSession(configs[0].Auth.SecretRef, data); err != nil {
		t.Fatal(err)
	}
	configs, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	resumed, report, err := NewHost(t.Context(), configs)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Loaded) != 1 {
		t.Fatalf("resume report=%#v", report)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	if refreshGrants.Load() != 1 {
		t.Fatalf("refresh grant count=%d", refreshGrants.Load())
	}
	configs, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(configs[0].Auth.SecretValue), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Token.AccessToken != "refreshed-access" || saved.Token.RefreshToken != "refresh-two" {
		t.Fatalf("refreshed OAuth session=%#v", saved.Token)
	}
	if authorizeCalls.Load() != 1 {
		t.Fatalf("restart unexpectedly reauthorized; authorize calls=%d", authorizeCalls.Load())
	}
	if err := store.ClearOAuthSession(configs[0].Auth.SecretRef); err != nil {
		t.Fatal(err)
	}
	if summary, err := store.Summaries(); err != nil || summary[0].AuthStatus != "needs_login" {
		t.Fatalf("logout summary=%#v err=%v", summary, err)
	}
}
