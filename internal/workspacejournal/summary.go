package workspacejournal

import (
	"fmt"
	"sort"
	"strings"
)

// SummaryBounds bounds a change summary.
type SummaryBounds struct {
	// MaxPaths bounds the number of distinct paths listed.
	MaxPaths int
}

const defaultSummaryMaxPaths = 50

func (b SummaryBounds) withDefaults() SummaryBounds {
	if b.MaxPaths <= 0 {
		b.MaxPaths = defaultSummaryMaxPaths
	}
	return b
}

// PathSummary summarizes recorded activity for one path.
type PathSummary struct {
	Path       string `json:"path"`
	Added      int    `json:"added_files"`    // files that did not exist before
	Modified   int    `json:"modified_files"` // completed mutations of an existing file
	Failed     int    `json:"failed_attempts"`
	Restored   int    `json:"restores"`
	LastSeq    uint64 `json:"last_seq"`
	LastAction string `json:"last_action"`
}

// Summary summarizes one session's journal, optionally up to a cursor.
type Summary struct {
	Session        string        `json:"session"`
	Cursor         string        `json:"cursor"`   // echoed cursor or newest cursor
	Observed       int           `json:"observed"` // folded entries considered
	Completed      int           `json:"completed"`
	Failed         int           `json:"failed"`
	Unknown        int           `json:"unknown"` // prepared without terminal status
	Restored       int           `json:"restored_ops"`
	Files          []PathSummary `json:"files"`
	TruncatedPaths bool          `json:"truncated_paths"`
	// Coverage note: the journal records file-tool mutations only.
	Coverage string `json:"coverage"`
}

const coverageNote = "records WriteFile/EditFile mutations only; Bash and external edits are unobserved, so absence of entries is not evidence the workspace is clean"

// Summarize folds the session journal and summarizes changes recorded up to
// and including the given cursor sequence ("" means everything retained).
func (s *Store) Summarize(session, cursor string, bounds SummaryBounds) (Summary, error) {
	ops, err := s.List(session)
	if err != nil {
		return Summary{}, err
	}
	filtered, _, err := foldAsOf(ops, cursor)
	if err != nil {
		return Summary{}, err
	}
	bounds = bounds.withDefaults()
	summary := Summary{Session: session, Cursor: cursor, Observed: len(filtered), Coverage: coverageNote}
	if cursor == "" {
		if newest, err := s.NewestCursor(session); err == nil {
			summary.Cursor = newest
		}
	}
	byPath := map[string]*PathSummary{}
	var order []string
	for _, op := range filtered {
		switch op.Status {
		case StatusCompleted, StatusRestored:
			summary.Completed++
			if op.Status == StatusRestored {
				summary.Restored++
			}
		case StatusFailed:
			summary.Failed++
		case StatusPrepared:
			summary.Unknown++
		}
		entry, exists := byPath[op.Path]
		if !exists {
			entry = &PathSummary{Path: op.Path}
			byPath[op.Path] = entry
			order = append(order, op.Path)
		}
		switch op.Status {
		case StatusCompleted, StatusRestored:
			entry.LastSeq = op.Seq
			entry.LastAction = op.Action
			if op.Pre == nil {
				entry.Added++
			} else {
				entry.Modified++
			}
			if op.Status == StatusRestored {
				entry.Restored++
			}
		case StatusFailed:
			entry.Failed++
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if byPath[order[i]].LastSeq != byPath[order[j]].LastSeq {
			return byPath[order[i]].LastSeq > byPath[order[j]].LastSeq
		}
		return order[i] < order[j]
	})
	for _, path := range order {
		if len(summary.Files) >= bounds.MaxPaths {
			summary.TruncatedPaths = true
			break
		}
		summary.Files = append(summary.Files, *byPath[path])
	}
	return summary, nil
}

// FormatSummary renders a compact human-readable summary for the model and
// CLI output.
func FormatSummary(summary Summary, indent string) string {
	var out strings.Builder
	status := fmt.Sprintf("%d completed, %d failed, %d unknown, %d restored", summary.Completed, summary.Failed, summary.Unknown, summary.Restored)
	if summary.Observed == 0 {
		out.WriteString(indent + "No file-tool changes recorded")
		if summary.Cursor != "" && summary.Cursor != "0" {
			out.WriteString(" since cursor " + summary.Cursor)
		}
		out.WriteString(".\n")
		out.WriteString(indent + "Coverage: " + coverageNote + ".\n")
		return out.String()
	}
	out.WriteString(fmt.Sprintf("%s%d path(s) touched: %s.\n", indent, len(summary.Files), status))
	if summary.TruncatedPaths {
		out.WriteString(fmt.Sprintf("%s(path list truncated at %d entries)\n", indent, len(summary.Files)))
	}
	for _, file := range summary.Files {
		parts := []string{}
		if file.Added > 0 {
			parts = append(parts, fmt.Sprintf("%d created", file.Added))
		}
		if file.Modified > 0 {
			parts = append(parts, fmt.Sprintf("%d modified", file.Modified))
		}
		if file.Failed > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", file.Failed))
		}
		if file.Restored > 0 {
			parts = append(parts, fmt.Sprintf("%d restored", file.Restored))
		}
		out.WriteString(fmt.Sprintf("%s  %s — %s\n", indent, file.Path, strings.Join(parts, ", ")))
	}
	out.WriteString(indent + "Coverage: " + coverageNote + ".\n")
	return out.String()
}
