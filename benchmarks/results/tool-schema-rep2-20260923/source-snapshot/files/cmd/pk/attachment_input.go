package main

import (
	"context"
	"fmt"
	"io"

	"github.com/pkyanam/pk/internal/attachments"
)

// loadPromptAttachments keeps selected file content attached only to the new
// user input, leaving any prior session prefix and cached context untouched.
func loadPromptAttachments(ctx context.Context, workspace, prompt string, paths []string) (string, []attachments.Attachment, error) {
	if len(paths) == 0 {
		return prompt, nil, nil
	}
	items, err := attachments.Load(ctx, workspace, paths, attachments.Limits{})
	if err != nil {
		return "", nil, err
	}
	return prompt + attachments.FormatPromptNote(items), items, nil
}

func writeAttachmentSummary(out io.Writer, items []attachments.Attachment) {
	for _, item := range items {
		fmt.Fprintf(out, "Attached %s (%s, %s", item.Path, item.Kind, item.ContentType)
		if item.Kind == attachments.PDFText {
			fmt.Fprintf(out, ", pages %d/%d extracted", item.PagesExtracted, item.PagesTotal)
		}
		if item.Truncated {
			fmt.Fprint(out, ", truncated to configured limit")
		}
		fmt.Fprintln(out, ")")
	}
}
