package sessionmanager

import (
	"math/rand"
	"strings"
	"testing"
)

func TestCompactMatchesReferenceNormalizationAndTruncation(t *testing.T) {
	inputs := []string{
		"",
		"  leading and trailing  ",
		"one\t two\nthree",
		"a\u2003b\u00a0c",
		"é漢🙂 — text",
		string([]byte{'a', 0xff, ' ', 0xfe, 'b'}),
		strings.Repeat("x", 1024),
		"a" + strings.Repeat(" \u2003\t", 1000) + "b",
	}
	for _, maxRunes := range []int{0, 1, 2, 3, 16, 96, 320} {
		for _, input := range inputs {
			if got, want := compact(input, maxRunes), compactReference(input, maxRunes); got != want {
				t.Errorf("compact(%q, %d) = %q, want %q", input[:min(len(input), 48)], maxRunes, got, want)
			}
		}
	}

	rng := rand.New(rand.NewSource(20260923))
	alphabet := []rune{'a', 'Z', '0', 'é', '漢', '🙂', ' ', '\t', '\n', '\r', '\u2003', '\u00a0'}
	for i := 0; i < 5000; i++ {
		length := rng.Intn(300)
		var input strings.Builder
		for j := 0; j < length; j++ {
			input.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		maxRunes := rng.Intn(64)
		if got, want := compact(input.String(), maxRunes), compactReference(input.String(), maxRunes); got != want {
			t.Fatalf("random case %d max=%d got %q want %q", i, maxRunes, got, want)
		}
	}
}

func compactReference(value string, maxRunes int) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "…"
}
