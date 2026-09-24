// Package diff renders bounded unified diffs for journal entries.
package diff

import (
	"fmt"
	"strings"
)

// Options bounds rendered diff output.
type Options struct {
	// MaxContextLines bounds context lines shown around each hunk.
	MaxContextLines int
	// MaxHunks bounds the number of hunks rendered; later hunks are elided.
	MaxHunks int
	// MaxHunkLines bounds the total number of rendered +/- lines per hunk.
	MaxHunkLines int
	// MaxOutputBytes bounds the total rendered output; larger renders are
	// truncated with an explicit notice line.
	MaxOutputBytes int
}

const (
	defaultContextLines = 3
	defaultMaxHunks     = 8
	defaultMaxHunkLines = 40
	defaultMaxBytes     = 16 << 10
)

func (o Options) withDefaults() Options {
	if o.MaxContextLines <= 0 {
		o.MaxContextLines = defaultContextLines
	}
	if o.MaxHunks <= 0 {
		o.MaxHunks = defaultMaxHunks
	}
	if o.MaxHunkLines <= 0 {
		o.MaxHunkLines = defaultMaxHunkLines
	}
	if o.MaxOutputBytes <= 0 {
		o.MaxOutputBytes = defaultMaxBytes
	}
	return o
}

// hunks is a simplified diff hunk.
type hunk struct {
	startA int // 1-based line number in the old file; 0 for empty old file
	startB int // 1-based line number in the new file
	lines  []hunkLine
}

type hunkLine struct {
	kind   byte // ' ', '-', '+'
	textA  string
	textB  string
}

// splitLines splits content into lines, dropping the trailing empty segment
// produced by a final newline. A trailing unterminated line is kept.
func splitLines(data string) []string {
	if data == "" {
		return nil
	}
	if strings.HasSuffix(data, "\n") {
		data = strings.TrimSuffix(data, "\n")
	}
	return strings.Split(data, "\n")
}

// Unified renders a unified diff between two text blobs under the given
// bounds. The header is caller-supplied (e.g. "--- a/file"). The output
// always ends with a truncation notice when any bound was hit.
func Unified(header string, before, after string, options Options) string {
	options = options.withDefaults()
	var out strings.Builder
	out.WriteString(header)
	out.WriteString("\n")
	beforeLines := splitLines(before)
	afterLines := splitLines(after)

	// Equal content: emit a short no-change note instead of an empty diff.
	if equalLines(beforeLines, afterLines) {
		out.WriteString("(contents are identical apart from trailing-newline differences)\n")
		return out.String()
	}

	hunks := computeHunks(beforeLines, afterLines, options.MaxContextLines)
	truncatedHunks := false
	shown := 0
	for _, h := range hunks {
		if shown >= options.MaxHunks {
			truncatedHunks = true
			break
		}
		hunkHeader := fmt.Sprintf("@@ -%d,%d +%d,%d @@", maxInt(h.startA, 1), countKind(h.lines, '-')+countKind(h.lines, ' ')+countKind(h.lines, 'b'), maxInt(h.startB, 1), countKind(h.lines, '+')+countKind(h.lines, ' ')+countKind(h.lines, 'b'))
		out.WriteString(hunkHeader)
		out.WriteString("\n")
		rendered := 0
		hunkTruncated := false
		for _, line := range h.lines {
			if line.kind == 'b' {
				continue // placeholder line in the prefix-suffix overlap
			}
			if rendered >= options.MaxHunkLines {
				hunkTruncated = true
				break
			}
			switch line.kind {
			case ' ':
				out.WriteString("  " + line.textA + "\n")
			case '-':
				out.WriteString("- " + line.textA + "\n")
			case '+':
				out.WriteString("+ " + line.textB + "\n")
			}
			rendered++
		}
		if hunkTruncated {
			out.WriteString(fmt.Sprintf("  … %d more changed line(s) in this hunk (hunk line limit %d)\n", countKind(h.lines, '-')+countKind(h.lines, '+')-countShown(h.lines, options.MaxHunkLines), options.MaxHunkLines))
		}
		shown++
	}
	if truncatedHunks {
		out.WriteString(fmt.Sprintf("… %d more hunk(s) not shown (hunk limit %d)\n", len(hunks)-options.MaxHunks, options.MaxHunks))
	}
	result := out.String()
	if len(result) > options.MaxOutputBytes {
		cut := result[:options.MaxOutputBytes]
		if index := strings.LastIndexByte(cut, '\n'); index > 0 {
			cut = cut[:index+1]
		}
		return cut + fmt.Sprintf("… diff output truncated at %d bytes\n", options.MaxOutputBytes)
	}
	return result
}

func countKind(lines []hunkLine, kind byte) int {
	count := 0
	for _, line := range lines {
		if line.kind == kind {
			count++
		}
	}
	return count
}

func countShown(lines []hunkLine, limit int) int {
	count := 0
	for _, line := range lines {
		if line.kind == 'b' {
			continue
		}
		if count >= limit {
			break
		}
		count++
	}
	return count
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// computeHunks performs an LCS-based line diff and groups changes into hunks
// with the requested context. This implementation is O(n*m) in lines; callers
// bound inputs to 2 MiB files and bounded rendering.
func computeHunks(before, after []string, context int) []hunk {
	n, m := len(before), len(after)
	// LCS table with the standard dynamic program.
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if before[i] == after[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}
	type edit struct {
		kind byte // ' ', '-', '+'
		a, b string
	}
	var edits []edit
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case before[i] == after[j]:
			edits = append(edits, edit{' ', before[i], after[j]})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			edits = append(edits, edit{'-', before[i], ""})
			i++
		default:
			edits = append(edits, edit{'+', "", after[j]})
			j++
		}
	}
	for ; i < n; i++ {
		edits = append(edits, edit{'-', before[i], ""})
	}
	for ; j < m; j++ {
		edits = append(edits, edit{'+', "", after[j]})
	}
	// Group edits into hunks with context.
	var hunks []hunk
	var current *hunk
	lastChange := -10
	lineA, lineB := 1, 1
	editIndex := 0
	for _, e := range edits {
		index := editIndex
		editIndex++
		switch e.kind {
		case ' ':
			if current != nil && index-lastChange <= context {
				current.lines = append(current.lines, hunkLine{kind: ' ', textA: e.a, textB: e.b})
			} else {
				if current != nil {
					hunks = append(hunks, *current)
					current = nil
				}
			}
			lineA++
			lineB++
		case '-':
			if current == nil {
				current = &hunk{startA: lineA, startB: lineB}
			}
			current.lines = append(current.lines, hunkLine{kind: '-', textA: e.a})
			lastChange = index
			lineA++
		case '+':
			if current == nil {
				current = &hunk{startA: lineA, startB: lineB}
			}
			current.lines = append(current.lines, hunkLine{kind: '+', textB: e.b})
			lastChange = index
			lineB++
		}
	}
	if current != nil {
		hunks = append(hunks, *current)
	}
	// Add trailing context lines to hunks where possible.
	return hunks
}
