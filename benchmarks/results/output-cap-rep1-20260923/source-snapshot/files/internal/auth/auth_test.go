package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type remapTransport struct{ target string }

func (t remapTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.URL.Scheme = "http"
	copy.URL.Host = strings.TrimPrefix(t.target, "http://")
	copy.Host = copy.URL.Host
	return http.DefaultTransport.RoundTrip(copy)
}

func testClient(server *httptest.Server) *http.Client {
	return &http.Client{Transport: remapTransport{target: server.URL}, Timeout: 2 * time.Second}
}

func jwt(exp int64, account string) string {
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"exp": exp, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account}})
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

func TestLoginDeviceFlowAndPrivatePersistence(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
		}
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode usercode request: %v", err)
			}
			if body["client_id"] != clientID {
				t.Errorf("unexpected client ID %q", body["client_id"])
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"device_auth_id":"device-id","user_code":"ABCD-EFGH","interval":"0"}`)
		case "/api/accounts/deviceauth/token":
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode poll request: %v", err)
			}
			if body["device_auth_id"] != "device-id" || body["user_code"] != "ABCD-EFGH" {
				t.Errorf("unexpected poll fields: %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"authorization_code":"one-time-code","code_verifier":"pkce-verifier"}`)
		case "/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse exchange form: %v", err)
			}
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_id") != clientID || r.Form.Get("code") != "one-time-code" || r.Form.Get("code_verifier") != "pkce-verifier" {
				t.Errorf("unexpected exchange form: %v", r.Form)
			}
			if r.Form.Get("redirect_uri") != issuerURL+"/deviceauth/callback" {
				t.Errorf("unexpected callback URI %q", r.Form.Get("redirect_uri"))
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id_token":%q,"access_token":%q,"refresh_token":"refresh-secret"}`, jwt(time.Now().Add(time.Hour).Unix(), "acct-123"), jwt(time.Now().Add(time.Hour).Unix(), "acct-123"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "auth.json")
	var output strings.Builder
	err := Login(context.Background(), LoginOptions{AuthFile: path, HTTPClient: testClient(server), Output: &output, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "ABCD-EFGH") || !strings.Contains(output.String(), issuerURL+"/codex/device") {
		t.Fatalf("missing device instructions: %q", output.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth file mode = %o, want 600", info.Mode().Perm())
	}
	if polls.Load() != 2 {
		t.Fatalf("poll count = %d, want 2", polls.Load())
	}
	credential, err := CredentialsFromCodex(path)
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccountID != "acct-123" || !strings.Contains(fmt.Sprintf("%+v", credential), "<redacted>") || !strings.Contains(fmt.Sprintf("%#v", credential), "<redacted>") {
		t.Fatalf("credential redaction/claims failed: %q", fmt.Sprintf("%+v", credential))
	}
}

func TestDeniedAndExpiredDeviceCodesFailWithoutPersistingOrLeaking(t *testing.T) {
	for _, test := range []struct{ name, code, want string }{
		{"denied", "authorization_declined", "denied"},
		{"expired", "device_code_expired", "expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/accounts/deviceauth/usercode" {
					io.WriteString(w, `{"device_auth_id":"device-id","user_code":"CODE","interval":"0"}`)
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintf(w, `{"error":%q,"error_description":"private-response-secret"}`, test.code)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "auth.json")
			err := Login(context.Background(), LoginOptions{AuthFile: path, HTTPClient: testClient(server), Output: io.Discard, PollInterval: time.Millisecond})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "private-response-secret") {
				t.Fatal("server response body leaked through error")
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("auth file should not exist after failed login, stat error = %v", statErr)
			}
		})
	}
}

func TestCancelWhileWaitingForDeviceApproval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/accounts/deviceauth/usercode" {
			io.WriteString(w, `{"device_auth_id":"device-id","user_code":"CODE","interval":"0"}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		cancel()
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "auth.json")
	err := Login(ctx, LoginOptions{AuthFile: path, HTTPClient: testClient(server), Output: io.Discard, PollInterval: time.Hour})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cancel") {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("auth file should not exist after cancellation")
	}
}

func TestExplicitCodexImportIsReadOnlyAndRejectsExpiredToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-auth.json")
	original := []byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"refresh_token":"do-not-rotate","account_id":"acct-import"}}`, jwt(time.Now().Add(-time.Hour).Unix(), "acct-import")))
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CredentialsFromCodex(path); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired import error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("Codex import changed its source file")
	}
}

func TestRefreshUsesOnlyPkOwnedTokenProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		var requestBody map[string]string
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode refresh JSON: %v", err)
		}
		if requestBody["grant_type"] != "refresh_token" || requestBody["client_id"] != clientID || requestBody["refresh_token"] != "refresh-secret" {
			t.Errorf("unexpected refresh JSON %v", requestBody)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rotated-refresh"}`, jwt(time.Now().Add(time.Hour).Unix(), "acct-refresh"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "auth.json")
	data := storedAuth{AuthMode: "chatgpt"}
	data.Tokens.AccessToken = jwt(time.Now().Add(20*time.Second).Unix(), "acct-refresh")
	data.Tokens.RefreshToken = "refresh-secret"
	data.Tokens.AccountID = "acct-refresh"
	if err := writeAuthFile(path, data); err != nil {
		t.Fatal(err)
	}
	got, err := CredentialsForPathWithClient(context.Background(), path, testClient(server))
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != "acct-refresh" || accountIDFromToken(got.AccessToken) != "acct-refresh" {
		t.Fatalf("unexpected refreshed credentials: %+v", got)
	}
	stored, err := readAuthFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Tokens.RefreshToken != "rotated-refresh" {
		t.Fatal("rotated refresh token was not persisted")
	}
}

func TestRefreshLockExcludesConcurrentOwnerAndHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	release, err := acquireRefreshLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if _, err := acquireRefreshLock(ctx, path); err == nil || !strings.Contains(strings.ToLower(err.Error()), "canceled") {
		t.Fatalf("second lock acquisition error = %v, want cancellation", err)
	}
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("lock returned before waiting for owner: %s", elapsed)
	}
}

func TestConcurrentRefreshCallsShareRotatedCredentials(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rotated-refresh"}`, jwt(time.Now().Add(time.Hour).Unix(), "acct-race"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "auth.json")
	data := storedAuth{AuthMode: "chatgpt"}
	data.Tokens.AccessToken = jwt(time.Now().Add(20*time.Second).Unix(), "acct-race")
	data.Tokens.RefreshToken = "refresh-secret"
	data.Tokens.AccountID = "acct-race"
	if err := writeAuthFile(path, data); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			credential, err := CredentialsForPathWithClient(context.Background(), path, testClient(server))
			if err == nil && (credential.AccountID != "acct-race" || accountIDFromToken(credential.AccessToken) != "acct-race") {
				err = fmt.Errorf("unexpected refreshed credential")
			}
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh request count = %d, want 1", calls.Load())
	}
	stored, err := readAuthFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Tokens.RefreshToken != "rotated-refresh" {
		t.Fatal("rotated token not shared with waiting request")
	}
}

func TestStatusReportsExpiredAndPrivateDirectoryIsNotRepermissioned(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "auth.json")
	data := storedAuth{AuthMode: "chatgpt"}
	data.Tokens.AccessToken = jwt(time.Now().Add(-time.Minute).Unix(), "acct-status")
	data.Tokens.AccountID = "acct-status"
	if err := os.WriteFile(path, mustJSON(data), 0o600); err != nil {
		t.Fatal(err)
	}
	status := statusForPath(path, "pk")
	if !status.LoggedIn || !status.Expired {
		t.Fatalf("status = %+v, want logged in and expired", status)
	}
	if err := writeAuthFile(filepath.Join(parent, "new-auth.json"), data); err != nil {
		t.Fatalf("custom auth path write failed: %v", err)
	}
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("parent mode changed to %o", info.Mode().Perm())
	}
}

func mustJSON(value any) []byte { data, _ := json.Marshal(value); return data }
