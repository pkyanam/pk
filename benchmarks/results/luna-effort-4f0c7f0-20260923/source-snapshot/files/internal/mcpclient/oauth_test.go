package mcpclient

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"
)

func TestOAuthCallbackAcceptsOnlyLoopbackCallbackAndReturnsState(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fetch := oauthCallbackFetcher(listener, func(context.Context, string) error { return nil })
	type result struct {
		r   *auth.AuthorizationResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := fetch(t.Context(), &auth.AuthorizationArgs{URL: "https://issuer.test/authorize"})
		done <- result{r, err}
	}()
	callbackURL := "http://" + listener.Addr().String() + "/oauth/callback?code=authcode&state=nonce&iss=https%3A%2F%2Fissuer.test"
	deadline := time.Now().Add(time.Second)
	var response *http.Response
	for time.Now().Before(deadline) {
		response, err = http.Get(callbackURL)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status=%d", response.StatusCode)
	}
	select {
	case got := <-done:
		if got.err != nil || got.r == nil || got.r.Code != "authcode" || got.r.State != "nonce" || got.r.Iss != "https://issuer.test" {
			t.Fatalf("callback result=%#v err=%v", got.r, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback fetcher did not return")
	}
}

func TestPersistingOAuthTokenSourceStoresRotatedToken(t *testing.T) {
	initial := &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	var saved oauthSession
	source := &persistingTokenSource{source: oauth2.StaticTokenSource(initial), config: oauth2.Config{ClientID: "client"}, save: func(session oauthSession) error { saved = session; return nil }}
	token, err := source.Token()
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "access" || saved.Token.RefreshToken != "refresh" || saved.Config.ClientID != "client" {
		t.Fatalf("token=%#v saved=%#v", token, saved)
	}
}

func TestOAuthStatusRequiresStoredSession(t *testing.T) {
	store := ConfigStore{Home: t.TempDir()}
	if err := store.AddOAuth(ServerConfig{ID: "remote", URL: "https://example.test/mcp"}); err != nil {
		t.Fatal(err)
	}
	summary, err := store.Summaries()
	if err != nil || len(summary) != 1 || summary[0].AuthStatus != "needs_login" {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
	servers, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var ref string
	for _, s := range servers {
		ref = s.Auth.SecretRef
	}
	data, _ := json.Marshal(oauthSession{Config: oauth2.Config{ClientID: "c", Endpoint: oauth2.Endpoint{TokenURL: "https://issuer.test/token"}}, Token: oauth2.Token{AccessToken: "a"}})
	if err := store.SetOAuthSession(ref, data); err != nil {
		t.Fatal(err)
	}
	summary, err = store.Summaries()
	if err != nil || summary[0].AuthStatus != "authenticated" {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
	if err := store.ClearOAuthSession(ref); err != nil {
		t.Fatal(err)
	}
	summary, err = store.Summaries()
	if err != nil || summary[0].AuthStatus != "needs_login" {
		t.Fatalf("after logout=%#v err=%v", summary, err)
	}
}

func TestStoredOAuthRefreshUsesCallerCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"unexpected","token_type":"Bearer","expires_in":3600}`))
		}
	}))
	defer server.Close()
	raw, err := json.Marshal(oauthSession{
		Config: oauth2.Config{ClientID: "fixture-client", Endpoint: oauth2.Endpoint{TokenURL: server.URL + "/token"}},
		Token:  oauth2.Token{AccessToken: "expired-access", RefreshToken: "refresh-fixture", Expiry: time.Now().Add(-time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	config := ServerConfig{ID: "remote", URL: "https://example.test/mcp", Auth: HTTPAuthConfig{Mode: "oauth", SecretRef: "fixture-ref", SecretValue: string(raw)}, SecretStoreHome: t.TempDir()}
	handler, listener, client, err := newOAuthHandler(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if listener != nil {
		t.Fatal("non-login OAuth handler unexpectedly opened a callback listener")
	}
	defer client.CloseIdleConnections()
	source, err := handler.TokenSource(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, tokenErr := source.Token()
		result <- tokenErr
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("OAuth refresh request did not start")
	}
	cancel()
	select {
	case tokenErr := <-result:
		if tokenErr == nil || !strings.Contains(tokenErr.Error(), "context canceled") {
			t.Fatalf("refresh result error=%v; want caller cancellation", tokenErr)
		}
	case <-time.After(time.Second):
		t.Fatal("OAuth refresh did not stop after caller cancellation")
	}
}

func TestConfiguredOAuthRedactionIncludesRotatedStoredToken(t *testing.T) {
	store := ConfigStore{Home: t.TempDir()}
	if err := store.AddOAuth(ServerConfig{ID: "remote", URL: "https://example.test/mcp"}); err != nil {
		t.Fatal(err)
	}
	servers, err := store.List()
	if err != nil || len(servers) != 1 {
		t.Fatalf("servers=%+v err=%v", servers, err)
	}
	config := servers[0]
	oldSession, err := json.Marshal(oauthSession{Config: oauth2.Config{ClientID: "fixture", Endpoint: oauth2.Endpoint{TokenURL: "https://issuer.test/token"}}, Token: oauth2.Token{AccessToken: "old-access", RefreshToken: "old-refresh"}})
	if err != nil {
		t.Fatal(err)
	}
	config.Auth.SecretValue = string(oldSession)
	rotated, err := json.Marshal(oauthSession{Config: oauth2.Config{ClientID: "fixture", Endpoint: oauth2.Endpoint{TokenURL: "https://issuer.test/token"}}, Token: oauth2.Token{AccessToken: "new-access", RefreshToken: "new-refresh"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetOAuthSession(config.Auth.SecretRef, rotated); err != nil {
		t.Fatal(err)
	}
	got := configuredCredentials(config)
	for _, want := range []string{"old-access", "old-refresh", "new-access", "new-refresh"} {
		found := false
		for _, value := range got {
			if value == want {
				found = true
			}
		}
		if !found {
			t.Errorf("credential %q missing from redaction values: %v", want, got)
		}
	}
}
