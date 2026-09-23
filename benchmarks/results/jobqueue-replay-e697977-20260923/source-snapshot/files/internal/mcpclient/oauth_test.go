package mcpclient

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
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
