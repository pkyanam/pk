package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkyanam/pk/internal/acp"
	"github.com/pkyanam/pk/internal/attachments"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

type acpImageManifest struct {
	Version      int             `json:"version"`
	SessionID    string          `json:"session_id"`
	InputID      string          `json:"input_id"`
	PromptSHA256 string          `json:"prompt_sha256"`
	Blocks       []acpSavedBlock `json:"blocks"`
}

type acpSavedBlock struct {
	Type     string `json:"type"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	MIMEType string `json:"mime_type,omitempty"`
	File     string `json:"file,omitempty"`
}

const maxACPImageManifestBytes = 64 << 10

// prepareACPImageInput returns the prompt text and persistence hooks for a
// turn. Paths are deterministic before runner.Run builds its durable payload;
// bytes are written only from BeforeInputPersist.
func prepareACPImageInput(sessionsDir string, turn acp.Turn, inputID string) (string, func(string, string) error, func(string, string), func(), error) {
	var err error
	sessionsDir, err = filepath.Abs(sessionsDir)
	if err != nil {
		return "", nil, nil, nil, fmt.Errorf("resolve session artifact directory: %w", err)
	}
	images := make([]acp.PromptBlock, 0, len(turn.Blocks))
	for _, block := range turn.Blocks {
		if block.Type == "image" {
			images = append(images, block)
		}
	}
	if len(images) == 0 {
		return turn.Prompt, nil, nil, func() {}, nil
	}
	if strings.TrimSpace(turn.SessionID) == "" || strings.TrimSpace(inputID) == "" {
		return "", nil, nil, nil, fmt.Errorf("ACP inline images require stable session and input IDs")
	}
	dir := attachments.PromptImageDir(sessionsDir, turn.SessionID, inputID)
	paths := make([]string, len(images))
	for i, block := range images {
		ext, ok := acpImageType(block.MIMEType)
		if !ok {
			return "", nil, nil, nil, fmt.Errorf("unsupported inline image MIME type")
		}
		paths[i] = filepath.Join(dir, fmt.Sprintf("image-%02d%s", i+1, ext))
	}
	var prompt strings.Builder
	savedBlocks := make([]acpSavedBlock, 0, len(turn.Blocks))
	imageIndex := 0
	for i, block := range turn.Blocks {
		if i > 0 {
			prompt.WriteByte('\n')
		}
		start := prompt.Len()
		switch block.Type {
		case "text":
			prompt.WriteString(block.Text)
		case "resource_link":
			label := block.Name
			if label == "" {
				label = block.URI
			}
			fmt.Fprintf(&prompt, "[User referenced resource: %s — %s]", label, block.URI)
		case "image":
			fmt.Fprintf(&prompt, "[User-provided image: %q (%s). Inspect only this explicitly provided image using ViewImage if needed.]", paths[imageIndex], block.MIMEType)
			imageIndex++
		}
		saved := acpSavedBlock{Type: block.Type, Start: start, End: prompt.Len()}
		if block.Type == "image" {
			saved.MIMEType, saved.File = block.MIMEType, filepath.Base(paths[imageIndex-1])
		}
		savedBlocks = append(savedBlocks, saved)
	}
	promptText := prompt.String()
	promptHash := sha256.Sum256([]byte(promptText))
	before := func(sessionID, actualInputID string) error {
		if sessionID != turn.SessionID || actualInputID != inputID {
			return fmt.Errorf("ACP inline image identity changed before persistence")
		}
		if err := ensurePrivatePageDir(sessionsDir, dir); err != nil {
			return fmt.Errorf("create private inline image directory: %w", err)
		}
		for i, block := range images {
			if err := writePrivateImage(paths[i], block.ImageData); err != nil {
				_ = os.RemoveAll(dir)
				return fmt.Errorf("persist inline image: %w", err)
			}
		}
		var totalPixels int64
		for i, block := range images {
			pixels, err := validateACPImage(context.Background(), paths[i], block.MIMEType)
			if err != nil {
				_ = os.RemoveAll(dir)
				return err
			}
			if pixels > operation.DefaultMaxViewImageSourcePixels-totalPixels {
				_ = os.RemoveAll(dir)
				return fmt.Errorf("inline images exceed ViewImage's aggregate %d-pixel limit", operation.DefaultMaxViewImageSourcePixels)
			}
			totalPixels += pixels
		}
		manifest := acpImageManifest{Version: 1, SessionID: sessionID, InputID: actualInputID, PromptSHA256: hex.EncodeToString(promptHash[:]), Blocks: savedBlocks}
		encoded, err := json.Marshal(manifest)
		if err != nil || len(encoded) > maxACPImageManifestBytes {
			_ = os.RemoveAll(dir)
			return fmt.Errorf("inline image metadata exceeds its size limit")
		}
		if err := writePrivateImage(filepath.Join(dir, "blocks.json"), encoded); err != nil {
			_ = os.RemoveAll(dir)
			return fmt.Errorf("persist inline image metadata: %w", err)
		}
		if err := syncDirectory(dir); err != nil {
			_ = os.RemoveAll(dir)
			return fmt.Errorf("sync inline image artifacts: %w", err)
		}
		return nil
	}
	after := func(sessionID, actualInputID string) {
		// The artifact now follows the durable input and is retained through
		// replay, session archive, restore, and purge.
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	return promptText, before, after, cleanup, nil
}

func writePrivateImage(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.Join(writeErr, syncErr, closeErr)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func validateACPImage(ctx context.Context, path, declaredMIME string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open inline image: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return 0, err
	}
	limits := attachments.DefaultLimits()
	if info.Size() <= 0 || info.Size() > limits.MaxImageBytes {
		_ = file.Close()
		return 0, fmt.Errorf("inline image exceeds ViewImage's source byte limit")
	}
	config, format, decodeErr := image.DecodeConfig(io.LimitReader(file, limits.MaxImageBytes+1))
	closeErr := file.Close()
	if decodeErr != nil || closeErr != nil {
		return 0, fmt.Errorf("invalid inline image: %w", errors.Join(decodeErr, closeErr))
	}
	actualMIME := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "webp": "image/webp", "bmp": "image/bmp", "tiff": "image/tiff"}[format]
	if actualMIME == "" || actualMIME != declaredMIME {
		return 0, fmt.Errorf("inline image MIME type does not match its image data")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > operation.DefaultMaxViewImageSourcePixels/config.Height {
		return 0, fmt.Errorf("inline image dimensions exceed ViewImage's %d-pixel limit", operation.DefaultMaxViewImageSourcePixels)
	}
	loaded, err := attachments.Load(ctx, filepath.Dir(path), []string{path}, attachments.Limits{MaxFiles: 1, MaxImageBytes: attachments.DefaultLimits().MaxImageBytes, MaxTotalBytes: attachments.DefaultLimits().MaxImageBytes})
	if err != nil {
		return 0, fmt.Errorf("validate inline image: %w", err)
	}
	if len(loaded) != 1 || loaded[0].Kind != attachments.Image || loaded[0].ContentType != declaredMIME {
		return 0, fmt.Errorf("inline image MIME type does not match its image signature")
	}
	return int64(config.Width) * int64(config.Height), nil
}

// acpSavedUserContent restores the original ordered text/image representation
// from a sidecar bound to the exact durable input payload. A missing sidecar is
// a legacy text-only input; a present but invalid sidecar is an integrity error.
func acpSavedUserContent(sessionsDir, sessionID, inputID, prompt string) ([]any, bool, error) {
	var err error
	sessionsDir, err = filepath.Abs(sessionsDir)
	if err != nil {
		return nil, true, fmt.Errorf("resolve ACP image artifact directory: %w", err)
	}
	dir := attachments.PromptImageDir(sessionsDir, sessionID, inputID)
	if err := checkACPImageDirectories(sessionsDir, sessionID, inputID); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("invalid ACP inline image directory: %w", err)
	}
	manifestPath := filepath.Join(dir, "blocks.json")
	info, err := os.Lstat(manifestPath)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxACPImageManifestBytes {
		return nil, true, fmt.Errorf("invalid ACP inline image metadata")
	}
	manifestFile, err := os.Open(manifestPath)
	if err != nil {
		return nil, true, fmt.Errorf("read ACP inline image metadata: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(manifestFile, maxACPImageManifestBytes+1))
	closeErr := manifestFile.Close()
	if readErr != nil || closeErr != nil || len(data) > maxACPImageManifestBytes {
		if readErr != nil || closeErr != nil {
			return nil, true, fmt.Errorf("read ACP inline image metadata: %w", errors.Join(readErr, closeErr))
		}
		return nil, true, fmt.Errorf("ACP inline image metadata exceeds its size limit")
	}
	var manifest acpImageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, true, fmt.Errorf("decode ACP inline image metadata: %w", err)
	}
	hash := sha256.Sum256([]byte(prompt))
	if manifest.Version != 1 || manifest.SessionID != sessionID || manifest.InputID != inputID || manifest.PromptSHA256 != hex.EncodeToString(hash[:]) || len(manifest.Blocks) == 0 || len(manifest.Blocks) > 256 {
		return nil, true, fmt.Errorf("ACP inline image metadata does not match the durable input")
	}
	content := make([]any, 0, len(manifest.Blocks))
	lastEnd := 0
	var totalPixels int64
	var totalBytes int64
	var imageCount int
	for _, block := range manifest.Blocks {
		if block.Start < lastEnd || block.Start < 0 || block.End < block.Start || block.End > len(prompt) {
			return nil, true, fmt.Errorf("invalid ACP inline image block offsets")
		}
		switch block.Type {
		case "text", "resource_link":
			content = append(content, map[string]any{"type": "text", "text": prompt[block.Start:block.End]})
		case "image":
			imageCount++
			if imageCount > acp.MaxInlineImages {
				return nil, true, fmt.Errorf("ACP inline image count exceeds its limit")
			}
			if filepath.Base(block.File) != block.File || !supportedACPImageMIME(block.MIMEType) {
				return nil, true, fmt.Errorf("invalid ACP inline image metadata")
			}
			path := filepath.Join(dir, block.File)
			fileInfo, err := os.Lstat(path)
			if err != nil || fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() || fileInfo.Size() <= 0 || fileInfo.Size() > acp.MaxInlineImageBytes || fileInfo.Size() > int64(acp.MaxInlineImageBytes)-totalBytes {
				return nil, true, fmt.Errorf("missing or invalid ACP inline image artifact")
			}
			pixels, err := validateACPImage(context.Background(), path, block.MIMEType)
			if err != nil {
				return nil, true, fmt.Errorf("invalid ACP inline image artifact: %w", err)
			}
			if pixels > operation.DefaultMaxViewImageSourcePixels-totalPixels {
				return nil, true, fmt.Errorf("ACP inline images exceed ViewImage's aggregate pixel limit")
			}
			totalPixels += pixels
			imageFile, err := os.Open(path)
			if err != nil {
				return nil, true, fmt.Errorf("read ACP inline image artifact: %w", err)
			}
			imageData, readErr := io.ReadAll(io.LimitReader(imageFile, acp.MaxInlineImageBytes+1))
			closeErr := imageFile.Close()
			if readErr != nil || closeErr != nil || len(imageData) == 0 || len(imageData) > acp.MaxInlineImageBytes {
				if readErr != nil || closeErr != nil {
					return nil, true, fmt.Errorf("read ACP inline image artifact: %w", errors.Join(readErr, closeErr))
				}
				return nil, true, fmt.Errorf("ACP inline image artifact exceeds its size limit")
			}
			totalBytes += int64(len(imageData))
			content = append(content, map[string]any{"type": "image", "mimeType": block.MIMEType, "data": base64.StdEncoding.EncodeToString(imageData)})
		default:
			return nil, true, fmt.Errorf("unsupported ACP inline image metadata block")
		}
		lastEnd = block.End
	}
	return content, true, nil
}

func checkACPImageDirectories(sessionsDir, sessionID, inputID string) error {
	sessionPath := attachments.SessionImageDir(sessionsDir, sessionID)
	for _, path := range []string{sessionsDir, filepath.Dir(sessionPath), sessionPath, attachments.PromptImageDir(sessionsDir, sessionID, inputID)} {
		if err := checkPrivateDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

func supportedACPImageMIME(value string) bool {
	_, ok := acpImageType(value)
	return ok
}

func acpImageType(mimeType string) (string, bool) {
	switch mimeType {
	case "image/png":
		return ".png", true
	case "image/jpeg":
		return ".jpg", true
	case "image/webp":
		return ".webp", true
	case "image/bmp":
		return ".bmp", true
	case "image/tiff":
		return ".tiff", true
	default:
		return "", false
	}
}
