package tasks

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

func watchCancel(parent context.Context, dir string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(dir, "cancel.request")); err == nil {
					_ = os.Remove(filepath.Join(dir, "cancel.request"))
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}
