package mcpclient

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const maxExportedToolName = 64

func exportName(serverID, toolName string) string {
	hash := sha256.Sum256([]byte(serverID + "\x00" + toolName))
	suffix := hex.EncodeToString(hash[:4])
	prefix := "mcp_" + serverID + "_"
	room := maxExportedToolName - len(prefix) - len(suffix) - 1
	var safe strings.Builder
	previousUnderscore := false
	for _, r := range strings.ToLower(toolName) {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !valid {
			r = '_'
		}
		if r == '_' && previousUnderscore {
			continue
		}
		if safe.Len() >= room {
			break
		}
		safe.WriteRune(r)
		previousUnderscore = r == '_'
	}
	base := strings.Trim(safe.String(), "_-.")
	if base == "" {
		base = "tool"
	}
	if len(base) > room {
		base = base[:room]
	}
	return prefix + base + "_" + suffix
}
