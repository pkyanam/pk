//go:build darwin && cgo

package clipboard

/*
#cgo LDFLAGS: -framework AppKit -framework Foundation
char *pk_clipboard_read_json(void);
void pk_clipboard_free(void *);
*/
import "C"

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

type NativeProvider struct{}

func (NativeProvider) Read(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	raw := C.pk_clipboard_read_json()
	if raw == nil {
		return Snapshot{}, errors.New("macOS clipboard helper returned no data")
	}
	defer C.pk_clipboard_free(unsafe.Pointer(raw))
	var value struct {
		Files   []string `json:"files"`
		Text    string   `json:"text"`
		Message string   `json:"message"`
		Image   struct {
			Type string `json:"type"`
			Data string `json:"data"`
		} `json:"image"`
	}
	if err := json.Unmarshal([]byte(C.GoString(raw)), &value); err != nil {
		return Snapshot{}, fmt.Errorf("decode macOS clipboard data: %w", err)
	}
	snapshot := Snapshot{Files: value.Files, Text: value.Text, Message: value.Message}
	if value.Image.Data != "" {
		decoded, err := base64.StdEncoding.DecodeString(value.Image.Data)
		if err != nil {
			return Snapshot{}, fmt.Errorf("decode clipboard image: %w", err)
		}
		if len(decoded) > MaxImageBytes {
			return Snapshot{}, fmt.Errorf("clipboard image exceeds %d bytes", MaxImageBytes)
		}
		snapshot.Image = &Image{ContentType: strings.ToLower(value.Image.Type), Bytes: decoded}
	}
	return snapshot, nil
}
