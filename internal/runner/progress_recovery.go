package runner

import (
	"regexp"
	"strings"
)

// A narrow fallback for models that announce a next action but return a final
// text-only response. The observer permits at most one recovery per run, only
// after real tool work. Ordinary answers and questions incur no extra request.
var nextActionSignoff = regexp.MustCompile(`(?i)(?:^|[.!]\s+)(?:let me|i['’]ll|i will|next,? i['’]ll)\s+(?:now\s+)?(?:examine|inspect|verify|check|read|investigate|compare|run|gather|trace|confirm|audit|search|review|locate|test)\b[^.!?]*[.!]?$`)

func unfinishedProgress(text, phase string) bool {
	text = strings.TrimSpace(text)
	if len(text) == 0 || len(text) > 800 || strings.Contains(text, "?") {
		return false
	}
	return phase == "commentary" || phase == "" && nextActionSignoff.MatchString(text)
}

const progressRecoveryNote = "[pk runtime] Your last reply announced further work but submitted no tool call. If authorized work remains, execute the next concrete step now; otherwise give the final result or a necessary question. Do not invent work or use filler calls. This automatic continuation is limited to once per run."
