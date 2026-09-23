//go:build darwin && cgo

package clipboard

/*
#include <stdlib.h>
#cgo LDFLAGS: -framework AppKit -framework Foundation
char *pk_clipboard_read_json(void);
void pk_clipboard_free(void *);
int pk_clipboard_write_text(const char *, size_t);
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
type NativeWriter struct{}

func (NativeWriter) WriteText(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateText(text); err != nil {
		return err
	}
	if len(text) == 0 {
		text = ""
	}
	data := []byte(text)
	var pointer unsafe.Pointer
	if len(data) == 0 {
		pointer = unsafe.Pointer(C.CString(""))
	} else {
		pointer = C.CBytes(data)
	}
	defer C.free(pointer)
	if C.pk_clipboard_write_text((*C.char)(pointer), C.size_t(len(data))) == 0 {
		return errors.New("macOS could not write text to the clipboard")
	}
	return nil
}

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
