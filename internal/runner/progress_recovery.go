package runner

import (
	"regexp"
	"strings"
)

// A narrow fallback for models that announce a next action but return a final
// text-only response. The observer permits one recovery, then rearms only
// after subsequent real tool work. Ordinary answers and questions incur
// no extra request.
var nextActionSignoff = regexp.MustCompile(`(?i)(?:^|[.!—]\s+)(?:let me|i['’]ll|i will|next,? i['’]ll)\s+(?:now\s+)?(?:examine|inspect|verify|check|read|investigate|compare|run|gather|trace|confirm|audit|search|review|locate|test)\b[^.!?]*[.!:]?$`)
var announcedSuite = regexp.MustCompile(`(?i)(?:^|[.!—]\s+)now\s+(?:the\s+)?(?:full\s+)?(?:verification|test|check)\s+suite\b[^.!?]*:`)
var committedActionPromise = regexp.MustCompile(`(?i)(?:^|[.!—]\s+)(?:let me(?: just| now)?|i['’]ll(?: just| now)?|i will(?: just| now)?|continuing:?|next,?)\s+(?:go ahead and\s+)?(?:push(?:ing)?|create|upload|publish|release|commit|build|rerun|resume|verify|inspect|check|run|test|confirm|apply|install|send)\b[^.!?]*[.!:]?$`)

func unfinishedProgress(text, phase string) bool {
	text = strings.TrimSpace(text)
	if len(text) == 0 || len(text) > 800 || strings.Contains(text, "?") {
		return false
	}
	return phase == "commentary" || phase == "" && (nextActionSignoff.MatchString(text) || announcedSuite.MatchString(text) || committedActionPromise.MatchString(text))
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
