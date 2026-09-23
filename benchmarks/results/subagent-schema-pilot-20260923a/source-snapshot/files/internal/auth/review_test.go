package auth

import (
	"fmt"
	"strings"
	"testing"
)

// Credential values are often included in diagnostic output by callers. Keep
// both access tokens and account routing IDs out of every fmt representation.
func TestCredentialFormattingDoesNotExposeSecrets(t *testing.T) {
	credential := Credential{AccessToken: "access-secret-value", AccountID: "account-secret-value"}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		got := fmt.Sprintf(format, credential)
		if strings.Contains(got, credential.AccessToken) || strings.Contains(got, credential.AccountID) {
			t.Errorf("format %q exposed credential data: %s", format, got)
		}
	}
}
