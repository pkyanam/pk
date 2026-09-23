package mcpclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
)

// SchemaFingerprint captures the configured servers and their discovered tool
// schemas. Environment values are included only inside a one-way digest so a
// secret change invalidates the saved tool host without being exposed.
func (h *Host) SchemaFingerprint() string {
	if h == nil {
		return ""
	}
	type fingerprintTool struct {
		Name        string         `json:"name"`
		ServerName  string         `json:"server_name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"input_schema"`
	}
	type fingerprintServer struct {
		ID               string            `json:"id"`
		URL              string            `json:"url,omitempty"`
		AuthMode         string            `json:"auth_mode,omitempty"`
		AuthHeader       string            `json:"auth_header,omitempty"`
		CredentialHash   string            `json:"credential_hash,omitempty"`
		Command          string            `json:"command"`
		Args             []string          `json:"args,omitempty"`
		EnvironmentHash  string            `json:"environment_hash"`
		WorkingDirectory string            `json:"working_directory,omitempty"`
		Tools            []fingerprintTool `json:"tools"`
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	servers := make([]fingerprintServer, 0, len(h.servers))
	for _, server := range h.servers {
		env, _ := json.Marshal(server.config.Env)
		envHash := sha256.Sum256(env)
		credential := ""
		if server.config.Auth.SecretValue != "" {
			credential = server.config.Auth.SecretValue
		}
		if server.config.Auth.BearerEnv != "" {
			credential, _ = os.LookupEnv(server.config.Auth.BearerEnv)
		}
		if server.config.Auth.HeaderValueEnv != "" {
			credential, _ = os.LookupEnv(server.config.Auth.HeaderValueEnv)
		}
		credentialHash := sha256.Sum256([]byte(credential))
		item := fingerprintServer{ID: server.config.ID, URL: server.config.URL, AuthMode: server.config.Auth.Mode, AuthHeader: server.config.Auth.HeaderName, CredentialHash: hex.EncodeToString(credentialHash[:]), Command: server.config.Command, Args: append([]string(nil), server.config.Args...), EnvironmentHash: hex.EncodeToString(envHash[:]), WorkingDirectory: server.config.WorkingDirectory, Tools: []fingerprintTool{}}
		for _, discovered := range h.ordered {
			if discovered.ServerID == server.config.ID {
				item.Tools = append(item.Tools, fingerprintTool{Name: discovered.Name, ServerName: discovered.ServerToolName, Description: discovered.Description, InputSchema: discovered.InputSchema})
			}
		}
		sort.Slice(item.Tools, func(i, j int) bool { return item.Tools[i].Name < item.Tools[j].Name })
		servers = append(servers, item)
	}
	if len(servers) == 0 {
		return ""
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].ID < servers[j].ID })
	data, err := json.Marshal(servers)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
