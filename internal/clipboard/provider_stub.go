//go:build !darwin || !cgo

package clipboard

import (
	"context"
	"errors"
)

type NativeProvider struct{}

func (NativeProvider) Read(context.Context) (Snapshot, error) {
	return Snapshot{}, errors.New("native clipboard file and image access is unavailable on this build; paste text or a path into the prompt")
}
