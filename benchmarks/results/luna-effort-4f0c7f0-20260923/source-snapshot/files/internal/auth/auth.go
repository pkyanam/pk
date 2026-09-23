package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	issuerURL        = "https://auth.openai.com"
	clientID         = "app_EMoamEEZ73f0CkXaXp7hrann"
	maxLoginDuration = 15 * time.Minute
	refreshSkew      = time.Minute
)

// Credentials contains only the access token and account routing ID needed by
// Codex-compatible API clients. Its fields are intentionally omitted from
// String and Go-syntax formatting.
type Credential struct {
	AccessToken string
	AccountID   string
}

// String and GoString deliberately redact credential values for logs.
func (Credential) String() string   { return "Credential{<redacted>}" }
func (Credential) GoString() string { return "auth.Credential{<redacted>}" }

// Status summarizes the local authentication state without exposing secrets.
type LoginStatus struct {
	LoggedIn  bool
	Expired   bool
	AccountID string
	ExpiresAt time.Time
	Source    string
}

// LoginOptions lets callers redirect the one-time code and override the HTTP
// client in tests. AuthFile is for pk's own credential file; it never points
// at or modifies the Codex CLI's auth file implicitly.
type LoginOptions struct {
	AuthFile     string
	HTTPClient   *http.Client
	Output       io.Writer
	Timeout      time.Duration
	PollInterval time.Duration
}

type storedAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token,omitempty"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token,omitempty"`
		AccountID    string `json:"account_id,omitempty"`
	} `json:"tokens"`
}

type deviceCode struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
	Interval     string `json:"interval"`
}

type pendingToken struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

type tokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// Login starts the official Codex device-code flow and saves the resulting
// ChatGPT credentials to pk's private auth file.
func Login(ctx context.Context, options LoginOptions) error {
	path, err := authPath(options.AuthFile)
	if err != nil {
		return err
	}
	client := options.HTTPClient
	if client == nil {
		client = safeHTTPClient(&http.Client{Timeout: 30 * time.Second})
	} else {
		client = safeHTTPClient(client)
	}
	output := options.Output
	if output == nil {
		output = os.Stdout
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = maxLoginDuration
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	code, err := requestDeviceCode(ctx, client)
	if err != nil {
		return err
	}
	if code.DeviceAuthID == "" || code.UserCode == "" {
		return errors.New("Codex device authorization returned incomplete code data")
	}
	if _, err := fmt.Fprintf(output, "Open %s/codex/device and enter code %s. Continue only if you started this login in pk.\n", issuerURL, code.UserCode); err != nil {
		return fmt.Errorf("print device authorization instructions: %w", err)
	}
	pending, err := pollDeviceToken(ctx, client, code, options.PollInterval)
	if err != nil {
		return err
	}
	if pending.AuthorizationCode == "" || pending.CodeVerifier == "" {
		return errors.New("Codex device authorization returned incomplete token exchange data")
	}
	redirectURI := issuerURL + "/deviceauth/callback"
	tokens, err := exchangeCode(ctx, client, pending.AuthorizationCode, pending.CodeVerifier, redirectURI)
	if err != nil {
		return err
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		return errors.New("Codex token exchange returned incomplete credentials")
	}
	data := storedAuth{AuthMode: "chatgpt"}
	data.Tokens.IDToken = tokens.IDToken
	data.Tokens.AccessToken = tokens.AccessToken
	data.Tokens.RefreshToken = tokens.RefreshToken
	data.Tokens.AccountID = accountIDFromToken(tokens.AccessToken)
	if data.Tokens.AccountID == "" {
		data.Tokens.AccountID = accountIDFromToken(tokens.IDToken)
	}
	if data.Tokens.AccountID == "" {
		return errors.New("Codex token exchange did not include a ChatGPT account ID")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("prepare pk credential directory: %w", err)
	}
	release, err := acquireRefreshLock(ctx, path)
	if err != nil {
		return fmt.Errorf("lock pk credentials: %w", err)
	}
	defer release()
	if err := writeAuthFile(path, data); err != nil {
		return fmt.Errorf("save pk credentials: %w", err)
	}
	return nil
}

// Credentials returns credentials from pk's own auth file. It refreshes that
// file when needed. Existing Codex CLI credentials are never read implicitly.
func CredentialsForPath(ctx context.Context, path string) (Credential, error) {
	return CredentialsForPathWithClient(ctx, path, http.DefaultClient)
}

// CredentialsForPathWithClient is CredentialsForPath with an injectable HTTP
// client, useful for callers that manage their own transport policy.
func CredentialsForPathWithClient(ctx context.Context, path string, client *http.Client) (Credential, error) {
	resolved, err := authPath(path)
	if err != nil {
		return Credential{}, err
	}
	release, err := acquireRefreshLock(ctx, resolved)
	if err != nil {
		return Credential{}, fmt.Errorf("lock pk credentials: %w", err)
	}
	defer release()
	data, err := readAuthFile(resolved)
	if err != nil {
		return Credential{}, err
	}
	access := strings.TrimSpace(data.Tokens.AccessToken)
	expires := tokenExpiry(access)
	if expires.IsZero() || time.Until(expires) > refreshSkew {
		return credentialsFrom(data, access)
	}
	if strings.TrimSpace(data.Tokens.RefreshToken) == "" {
		return Credential{}, errors.New("pk access token has expired and no refresh token is available; run `pk login`")
	}
	refreshed, err := refresh(ctx, safeHTTPClient(client), data.Tokens.RefreshToken)
	if err != nil {
		return Credential{}, err
	}
	if refreshed.AccessToken == "" {
		return Credential{}, errors.New("Codex token refresh returned no access token")
	}
	data.Tokens.AccessToken = refreshed.AccessToken
	if refreshed.IDToken != "" {
		data.Tokens.IDToken = refreshed.IDToken
	}
	if refreshed.RefreshToken != "" {
		data.Tokens.RefreshToken = refreshed.RefreshToken
	}
	if data.Tokens.AccountID == "" {
		data.Tokens.AccountID = accountIDFromToken(refreshed.AccessToken)
	}
	if data.Tokens.AccountID == "" {
		return Credential{}, errors.New("Codex token refresh did not include a ChatGPT account ID")
	}
	if err := writeAuthFile(resolved, data); err != nil {
		return Credential{}, fmt.Errorf("save refreshed pk credentials: %w", err)
	}
	return credentialsFrom(data, refreshed.AccessToken)
}

// Credentials is the default-path convenience used by the runtime.
func Credentials(ctx context.Context) (Credential, error) { return CredentialsForPath(ctx, "") }

// RefreshCredentials forces a refresh of pk-owned credentials. It is intended
// for recovery after an API explicitly rejects an otherwise current token.
func RefreshCredentials(ctx context.Context) (Credential, error) {
	return RefreshCredentialsForPath(ctx, "", http.DefaultClient)
}

// RefreshCredentialsForPath forces a refresh from a specific pk-owned file.
func RefreshCredentialsForPath(ctx context.Context, path string, client *http.Client) (Credential, error) {
	resolved, err := authPath(path)
	if err != nil {
		return Credential{}, err
	}
	release, err := acquireRefreshLock(ctx, resolved)
	if err != nil {
		return Credential{}, fmt.Errorf("lock pk credentials: %w", err)
	}
	defer release()
	data, err := readAuthFile(resolved)
	if err != nil {
		return Credential{}, err
	}
	if data.Tokens.RefreshToken == "" {
		return Credential{}, errors.New("pk auth file has no refresh token; run `pk login`")
	}
	refreshed, err := refresh(ctx, safeHTTPClient(client), data.Tokens.RefreshToken)
	if err != nil {
		return Credential{}, err
	}
	if refreshed.AccessToken == "" {
		return Credential{}, errors.New("Codex token refresh returned no access token")
	}
	data.Tokens.AccessToken = refreshed.AccessToken
	if refreshed.IDToken != "" {
		data.Tokens.IDToken = refreshed.IDToken
	}
	if refreshed.RefreshToken != "" {
		data.Tokens.RefreshToken = refreshed.RefreshToken
	}
	if data.Tokens.AccountID == "" {
		data.Tokens.AccountID = accountIDFromToken(refreshed.AccessToken)
	}
	if data.Tokens.AccountID == "" {
		return Credential{}, errors.New("Codex token refresh did not include a ChatGPT account ID")
	}
	if err := writeAuthFile(resolved, data); err != nil {
		return Credential{}, fmt.Errorf("save refreshed pk credentials: %w", err)
	}
	return credentialsFrom(data, refreshed.AccessToken)
}

// CredentialsFromCodex reads an existing Codex auth file without changing or
// refreshing it. The caller must opt in by supplying the path explicitly.
func CredentialsFromCodex(path string) (Credential, error) {
	data, err := readAuthFile(path)
	if err != nil {
		return Credential{}, err
	}
	access := strings.TrimSpace(data.Tokens.AccessToken)
	if expires := tokenExpiry(access); !expires.IsZero() && time.Until(expires) <= 0 {
		return Credential{}, errors.New("Codex auth file access token has expired; run `codex login` to refresh it")
	}
	return credentialsFrom(data, access)
}

// Status reports pk's default local auth state without performing network I/O.
func Status() LoginStatus { return statusForPath(defaultAuthFile(), "pk") }

// StatusFromCodex reports an explicitly selected Codex auth file without
// modifying it.
func StatusFromCodex(path string) LoginStatus { return statusForPath(path, "codex") }

// Logout removes only pk's own auth file.
func Logout() error { return LogoutForPath("") }

// LogoutForPath removes only the specified pk-owned auth file.
func LogoutForPath(path string) error {
	resolved, err := authPath(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(resolved); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	release, err := acquireRefreshLock(context.Background(), resolved)
	if err != nil {
		return fmt.Errorf("lock pk credentials: %w", err)
	}
	defer release()
	if err := os.Remove(resolved); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove pk credentials: %w", err)
	}
	return nil
}

func requestDeviceCode(ctx context.Context, client *http.Client) (deviceCode, error) {
	var response deviceCode
	body := struct {
		ClientID string `json:"client_id"`
	}{clientID}
	err := postJSON(ctx, client, issuerURL+"/api/accounts/deviceauth/usercode", body, &response, "request Codex device code")
	if err != nil {
		return deviceCode{}, err
	}
	return response, nil
}

func pollDeviceToken(ctx context.Context, client *http.Client, code deviceCode, intervalOverride time.Duration) (pendingToken, error) {
	interval, _ := time.ParseDuration(strings.TrimSpace(code.Interval) + "s")
	if intervalOverride > 0 {
		interval = intervalOverride
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.NewTimer(maxLoginDuration)
	defer deadline.Stop()
	for {
		var pending pendingToken
		body := struct {
			DeviceAuthID string `json:"device_auth_id"`
			UserCode     string `json:"user_code"`
		}{code.DeviceAuthID, code.UserCode}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, issuerURL+"/api/accounts/deviceauth/token", jsonReader(body))
		if err != nil {
			return pendingToken{}, fmt.Errorf("poll Codex device authorization: %w", err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return pendingToken{}, fmt.Errorf("poll Codex device authorization: %w", err)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if readErr != nil {
			return pendingToken{}, errors.New("read Codex device authorization response")
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			if err := json.Unmarshal(responseBody, &pending); err != nil {
				return pendingToken{}, errors.New("decode Codex device authorization response")
			}
			return pending, nil
		}
		if response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusNotFound {
			var failure struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			_ = json.Unmarshal(responseBody, &failure)
			switch strings.ToLower(failure.Error) {
			case "authorization_declined", "access_denied", "authorization_denied":
				return pendingToken{}, errors.New("Codex device authorization was denied")
			case "expired_token", "device_code_expired", "authorization_expired":
				return pendingToken{}, errors.New("Codex device authorization code expired")
			}
			switch strings.ToLower(failure.Code) {
			case "expired_token", "device_code_expired", "authorization_expired":
				return pendingToken{}, errors.New("Codex device authorization code expired")
			}
			return pendingToken{}, fmt.Errorf("Codex device authorization failed with HTTP %d", response.StatusCode)
		}
		wait := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			wait.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return pendingToken{}, errors.New("Codex device authorization expired or timed out")
			}
			return pendingToken{}, ctx.Err()
		case <-deadline.C:
			wait.Stop()
			return pendingToken{}, errors.New("Codex device authorization expired or timed out")
		case <-wait.C:
		}
	}
}

func exchangeCode(ctx context.Context, client *http.Client, code, verifier, redirect string) (tokenResponse, error) {
	values := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
	return postTokenForm(ctx, client, values, "exchange Codex authorization code")
}

func refresh(ctx context.Context, client *http.Client, refreshToken string) (tokenResponse, error) {
	values := map[string]string{"grant_type": "refresh_token", "client_id": clientID, "refresh_token": refreshToken}
	return postTokenJSON(ctx, client, values, "refresh pk credentials")
}

func postTokenJSON(ctx context.Context, client *http.Client, values map[string]string, operation string) (tokenResponse, error) {
	encoded, err := json.Marshal(values)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%s: %w", operation, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, issuerURL+"/oauth/token", strings.NewReader(string(encoded)))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%s: %w", operation, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%s: %w", operation, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("%s failed with HTTP %d", operation, response.StatusCode)
	}
	var result tokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return tokenResponse{}, fmt.Errorf("%s: invalid token response", operation)
	}
	return result, nil
}

func postTokenForm(ctx context.Context, client *http.Client, values url.Values, operation string) (tokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, issuerURL+"/oauth/token", strings.NewReader(values.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%s: %w", operation, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%s: %w", operation, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("%s failed with HTTP %d", operation, response.StatusCode)
	}
	var result tokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return tokenResponse{}, fmt.Errorf("%s: invalid token response", operation)
	}
	return result, nil
}

func postJSON(ctx context.Context, client *http.Client, endpoint string, body any, target any, operation string) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s failed with HTTP %d", operation, response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return fmt.Errorf("%s: invalid response", operation)
	}
	return nil
}

func credentialsFrom(data storedAuth, token string) (Credential, error) {
	if data.AuthMode != "" && data.AuthMode != "chatgpt" {
		return Credential{}, errors.New("auth file is not a ChatGPT subscription login")
	}
	if token == "" {
		return Credential{}, errors.New("no Codex access token; run `pk login` or explicitly import Codex credentials")
	}
	accountID := strings.TrimSpace(data.Tokens.AccountID)
	if accountID == "" {
		accountID = accountIDFromToken(token)
	}
	if accountID == "" {
		return Credential{}, errors.New("Codex account ID is missing from credentials")
	}
	return Credential{AccessToken: token, AccountID: accountID}, nil
}

func statusForPath(path, source string) LoginStatus {
	resolved, err := authPath(path)
	if err != nil {
		return LoginStatus{Source: source}
	}
	data, err := readAuthFile(resolved)
	if err != nil {
		return LoginStatus{Source: source}
	}
	creds, err := credentialsFrom(data, data.Tokens.AccessToken)
	if err != nil {
		return LoginStatus{Source: source}
	}
	expires := tokenExpiry(creds.AccessToken)
	return LoginStatus{LoggedIn: true, Expired: !expires.IsZero() && time.Until(expires) <= 0, AccountID: creds.AccountID, ExpiresAt: expires, Source: source}
}

func safeHTTPClient(client *http.Client) *http.Client {
	copy := *client
	previous := client.CheckRedirect
	copy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 {
			prior := via[len(via)-1].URL
			if !strings.EqualFold(prior.Host, req.URL.Host) || (prior.Scheme == "https" && req.URL.Scheme != "https") {
				return errors.New("refusing to redirect Codex authentication request to another origin")
			}
		}
		if previous != nil {
			return previous(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 authentication redirects")
		}
		return nil
	}
	if copy.Timeout == 0 {
		copy.Timeout = 30 * time.Second
	}
	return &copy
}

func authPath(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return filepath.Clean(path), nil
	}
	return defaultAuthFile(), nil
}

func defaultAuthFile() string {
	if home := strings.TrimSpace(os.Getenv("PK_HOME")); home != "" {
		return filepath.Join(home, "auth.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".pk", "auth.json")
	}
	return filepath.Join(home, ".pk", "auth.json")
}

func readAuthFile(path string) (storedAuth, error) {
	file, err := os.Open(path)
	if err != nil {
		return storedAuth{}, fmt.Errorf("open auth file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return storedAuth{}, errors.New("inspect auth file")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return storedAuth{}, errors.New("auth file must be a regular file with private permissions (0600)")
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return storedAuth{}, errors.New("read auth file")
	}
	var auth storedAuth
	if err := json.Unmarshal(data, &auth); err != nil {
		return storedAuth{}, errors.New("invalid auth file JSON")
	}
	return auth, nil
}

func writeAuthFile(path string, auth storedAuth) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if filepath.Clean(path) == filepath.Clean(defaultAuthFile()) {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if dirInfo.Mode().Perm()&0o077 != 0 {
			return errors.New("pk auth directory must have private permissions (0700)")
		}
	}
	data, err := json.Marshal(auth)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".auth-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func tokenExpiry(token string) time.Time {
	claims := tokenClaims(token)
	if claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

func accountIDFromToken(token string) string { return tokenClaims(token).Auth.ChatGPTAccountID }

type jwtClaims struct {
	Exp  int64 `json:"exp"`
	Auth struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

func tokenClaims(token string) jwtClaims {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}
	}
	var claims jwtClaims
	_ = json.Unmarshal(payload, &claims)
	return claims
}

// jsonReader is defined separately to keep device-code request construction
// independent of any third-party JSON package.
func jsonReader(value any) io.Reader {
	data, _ := json.Marshal(value)
	return strings.NewReader(string(data))
}
