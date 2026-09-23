package attachments

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// SessionImageDir owns inline images imported for a session. Hashing IDs keeps
// protocol-supplied identifiers out of filesystem paths.
func SessionImageDir(sessionsDir, sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(sessionsDir, "inline-images", hex.EncodeToString(sum[:]))
}

// PromptImageDir isolates cleanup of an unpersisted input from earlier images.
func PromptImageDir(sessionsDir, sessionID, inputID string) string {
	sum := sha256.Sum256([]byte(inputID))
	return filepath.Join(SessionImageDir(sessionsDir, sessionID), hex.EncodeToString(sum[:]))
}
