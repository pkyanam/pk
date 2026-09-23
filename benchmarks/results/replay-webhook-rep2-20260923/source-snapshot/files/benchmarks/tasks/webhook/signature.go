package webhook

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// Sign returns the lowercase hexadecimal HMAC-SHA256 of the exact request body.
func Sign(secret, body []byte) string {
	return "" // TODO: Calculate HMAC-SHA256 without changing body bytes.
}

// VerifySignature accepts an optional "sha256=" prefix and compares signatures
// without leaking a byte-by-byte timing difference.
func VerifySignature(secret, body []byte, header string) bool {
	header = strings.TrimPrefix(strings.TrimSpace(header), "sha256=")
	provided, err := hex.DecodeString(header)
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	expected, err := hex.DecodeString(Sign(secret, body))
	if err != nil || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare(provided, expected) == 1
}
