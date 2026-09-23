package attachments

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// SessionPDFPageDir returns the managed artifact directory for rendered PDF
// pages owned by a session. IDs are digested so they cannot become paths.
func SessionPDFPageDir(sessionsDir, sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(sessionsDir, "pdf-pages", hex.EncodeToString(sum[:]))
}

// PromptPDFPageDir isolates the generated pages for one input, allowing a
// failed prompt setup to clean up without touching earlier attachments.
func PromptPDFPageDir(sessionsDir, sessionID, inputID string) string {
	sum := sha256.Sum256([]byte(inputID))
	return filepath.Join(SessionPDFPageDir(sessionsDir, sessionID), hex.EncodeToString(sum[:]))
}
