package update

import "strings"

// buildEnvironment keeps the developer's toolchain/proxy settings while
// removing pk launcher/session overrides that can redirect Go tests or health
// checks into the currently installed release.
func buildEnvironment(values []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		key, _, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(key, "PK_") {
			continue
		}
		if key == "CODEX_HOME" {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}
