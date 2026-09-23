package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/runner"
)

// loadPromptAttachments keeps selected file content attached only to the new
// user input, leaving any prior session prefix and cached context untouched.
func loadPromptAttachments(ctx context.Context, workspace, prompt string, paths []string) (string, []attachments.Attachment, error) {
	return loadPromptAttachmentsWithPageDir(ctx, workspace, prompt, paths, "")
}

func loadPromptAttachmentsWithPageDir(ctx context.Context, workspace, prompt string, paths []string, pageDir string) (string, []attachments.Attachment, error) {
	if len(paths) == 0 {
		return prompt, nil, nil
	}
	limits := attachments.Limits{}
	if pageDir != "" {
		limits.PDFPageImageDir = pageDir
	}
	items, err := attachments.Load(ctx, workspace, paths, limits)
	if err != nil {
		return "", nil, err
	}
	return prompt + attachments.FormatPromptNote(items), items, nil
}

func prepareAttachmentIdentity(options *runner.Options) (promptID, pageDir string, cleanup func(), err error) {
	if strings.TrimSpace(options.SessionDir) == "" {
		return "", "", nil, fmt.Errorf("session directory is required for attachments")
	}
	sessionsDir, err := filepath.Abs(options.SessionDir)
	if err != nil {
		return "", "", nil, err
	}
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		return "", "", nil, err
	}
	rootInfo, err := os.Lstat(sessionsDir)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", "", nil, fmt.Errorf("session store %q is not a real directory", sessionsDir)
	}
	if err := os.Chmod(sessionsDir, 0o700); err != nil {
		return "", "", nil, err
	}
	sessionsDir, err = filepath.EvalSymlinks(sessionsDir)
	if err != nil {
		return "", "", nil, err
	}
	promptID, err = newPresentationPromptID()
	if err != nil {
		return "", "", nil, err
	}
	sessionID := strings.TrimSpace(options.SessionID)
	if sessionID == "" {
		if options.PreallocatedNewID == "" {
			options.PreallocatedNewID, err = runner.NewSessionID()
			if err != nil {
				return "", "", nil, err
			}
		}
		sessionID = options.PreallocatedNewID
	}
	pageDir = attachments.PromptPDFPageDir(sessionsDir, sessionID, promptID)
	if err := ensurePrivatePageDir(sessionsDir, pageDir); err != nil {
		return "", "", nil, err
	}
	sessionPages := attachments.SessionPDFPageDir(sessionsDir, sessionID)
	pagesRoot := filepath.Dir(sessionPages)
	return promptID, pageDir, func() {
		_ = os.RemoveAll(pageDir)
		_ = os.Remove(sessionPages)
		_ = os.Remove(pagesRoot)
	}, nil
}

func ensurePrivatePageDir(sessionsDir, target string) error {
	root, err := filepath.Abs(sessionsDir)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("rendered page directory escaped session store")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("session store %q is not a real directory", root)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	if err := checkPrivateDirectory(root); err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		if err := checkPrivateDirectory(current); err != nil {
			return err
		}
	}
	return nil
}

func checkPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("rendered page directory %q is not a private real directory", path)
	}
	return nil
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
		if len(item.RenderedPDFPages) > 0 {
			fmt.Fprintf(out, ", %d PDF pages rendered", len(item.RenderedPDFPages))
		}
		if item.RenderNotice != "" {
			fmt.Fprintf(out, ", %s", item.RenderNotice)
		}
		fmt.Fprintln(out, ")")
	}
}

func hasRenderedPDFPages(items []attachments.Attachment) bool {
	for _, item := range items {
		if len(item.RenderedPDFPages) > 0 {
			return true
		}
	}
	return false
}
