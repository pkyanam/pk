//go:build !darwin || !cgo

package clipboard

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type NativeProvider struct{}
type NativeWriter struct{}

func (NativeProvider) Read(context.Context) (Snapshot, error) {
	return Snapshot{}, errors.New("native clipboard file and image access is unavailable on this build; paste text or a path into the prompt")
}

func (NativeWriter) WriteText(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateText(text); err != nil {
		return err
	}
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"pbcopy"}}
	case "windows":
		candidates = [][]string{{"clip.exe"}, {"clip"}}
	case "linux", "freebsd", "openbsd", "netbsd":
		candidates = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	default:
		return fmt.Errorf("native clipboard text write is unavailable on %s", runtime.GOOS)
	}
	var lastErr error
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate[0])
		if err != nil {
			lastErr = err
			continue
		}
		command := exec.CommandContext(ctx, path, candidate[1:]...)
		command.Stdin = strings.NewReader(text)
		if err := command.Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return fmt.Errorf("write text to native clipboard: %w", lastErr)
	}
	return errors.New("native clipboard text write is unavailable; install pbcopy, wl-copy, xclip, or xsel for this platform")
}
