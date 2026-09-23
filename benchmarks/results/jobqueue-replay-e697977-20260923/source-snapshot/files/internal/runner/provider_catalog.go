package runner

import (
	"context"
	"errors"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/session"
)

func validateProviderFingerprint(snapshot ContextSnapshot, current string) error {
	if snapshot.ProviderFingerprint == current {
		return nil
	}
	return errors.New("model provider or API endpoint changed; restore the original provider configuration or start a new session")
}

func validateProviderSelection(snapshot ContextSnapshot, current string) error {
	// Older custom-provider snapshots may have a fingerprint but no ID; their
	// endpoint/key identity is still protected by the fingerprint check.
	if snapshot.ProviderID == "" || snapshot.ProviderID == current {
		return nil
	}
	return errors.New("session uses provider " + snapshot.ProviderID + "; select that provider or start a new session")
}

// LoadSavedProvider returns the provider ID and fingerprint frozen into a
// session snapshot. `saved` is false when no snapshot exists (legacy session).
func LoadSavedProvider(ctx context.Context, sessionDir, sessionID, workspace string) (id, fingerprint string, saved bool, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", false, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return "", "", false, nil
	}
	snapshot, err := defaultContextSnapshotStore(sessionDir, workspace).LoadContext(ctx, session.ID(sessionID))
	if err != nil {
		if isMissingContextSnapshot(err) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	return snapshot.ProviderID, snapshot.ProviderFingerprint, true, nil
}
