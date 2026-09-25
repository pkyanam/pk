package runner

import (
	"regexp"
	"strings"
)

// The fallback only considers the final sentence, so an earlier promise does
// not override a later factual result. Dots inside paths, URLs, and versions
// are allowed in that sentence.
var sentenceBoundary = regexp.MustCompile(`[.!—]\s+`)
var nextActionPromise = regexp.MustCompile(`(?i)^(?:let me(?: just| now)?|i['’]ll(?: just| now)?|i will(?: just| now)?|continuing:?|next,? i['’]ll|next,?)\s+(?:go ahead and\s+)?(?:examine|inspect|verify|check|read|investigate|compare|run|gather|trace|confirm|audit|search|review|locate|test|push(?:ing)?|create|upload|publish|release|commit|build|rerun|resume|apply|install|send)\b[^!?]*[.!:]?$`)
var announcedSuite = regexp.MustCompile(`(?i)^now\s+(?:the\s+)?(?:full\s+)?(?:verification|test|check)\s+suite\b[^!?]*:`)

func unfinishedProgress(text, phase string) bool {
	text = strings.TrimSpace(text)
	if len(text) == 0 || phase == "final_answer" || strings.Contains(text, "?") {
		return false
	}
	boundaries := sentenceBoundary.FindAllStringIndex(text, -1)
	if len(boundaries) > 0 {
		text = strings.TrimSpace(text[boundaries[len(boundaries)-1][1]:])
	}
	return announcedSuite.MatchString(text) || nextActionPromise.MatchString(text)
}

func resetProgressRecoveryForExternalInput(external, internalRecovery bool, sawToolWork, recoveredProgress *bool) {
	if external && !internalRecovery {
		*sawToolWork, *recoveredProgress = false, false
	}
}

func resetProgressRecoveryForToolWork(realToolCall bool, sawToolWork, recoveredProgress *bool) {
	if realToolCall {
		*sawToolWork, *recoveredProgress = true, false
	}
}

const progressRecoveryNote = "[pk runtime] Your last reply announced another action but ended without a tool call. If work remains, take the next concrete step now; otherwise give the final answer or ask a necessary question. Do not invent work or make filler calls."
