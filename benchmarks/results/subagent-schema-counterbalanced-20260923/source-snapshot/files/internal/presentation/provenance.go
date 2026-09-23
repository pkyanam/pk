// Package presentation persists small, presentation-only metadata associated
// with durable session inputs. It never stores prompt or attachment contents.
package presentation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pkyanam/pk/internal/attachments"
)

const (
	Version        = 1
	MaxAttachments = 8
	MaxNameBytes   = 256
	MaxTypeBytes   = 128
	maxRecordBytes = 16 << 10
)

type Attachment struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	ContentType    string `json:"content_type,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
	PagesExtracted int    `json:"pages_extracted,omitempty"`
	PagesTotal     int    `json:"pages_total,omitempty"`
}

// Record contains hashes and chips only. PrefixBytes identifies the user's
// typed prompt before the generated attachment note in the effective payload.
type Record struct {
	Version                int          `json:"version"`
	PrefixBytes            int          `json:"prefix_bytes"`
	PromptPrefixSHA256     string       `json:"prompt_prefix_sha256"`
	EffectivePayloadSHA256 string       `json:"effective_payload_sha256"`
	Attachments            []Attachment `json:"attachments"`
}

func NewRecord(userText, effectivePayload string, items []attachments.Attachment) (Record, error) {
	if len(items) == 0 || len(items) > MaxAttachments {
		return Record{}, fmt.Errorf("presentation attachment count must be between 1 and %d", MaxAttachments)
	}
	if len(userText) > len(effectivePayload) || !utf8.ValidString(userText) || !utf8.ValidString(effectivePayload) || !strings.HasPrefix(effectivePayload, userText) {
		return Record{}, errors.New("effective prompt does not start with the original user prompt")
	}
	chips := make([]Attachment, 0, len(items))
	for _, item := range items {
		name := path.Base(strings.ReplaceAll(item.Path, "\\", "/"))
		if name == "." || name == "/" || name == "" {
			return Record{}, errors.New("attachment has no display filename")
		}
		chip := Attachment{Name: boundedUTF8(name, MaxNameBytes), Kind: string(item.Kind), ContentType: boundedUTF8(item.ContentType, MaxTypeBytes), Truncated: item.Truncated, PagesExtracted: item.PagesExtracted, PagesTotal: item.PagesTotal}
		if err := validateChip(chip); err != nil {
			return Record{}, err
		}
		chips = append(chips, chip)
	}
	prefixHash := sha256.Sum256([]byte(userText))
	payloadHash := sha256.Sum256([]byte(effectivePayload))
	record := Record{Version: Version, PrefixBytes: len(userText), PromptPrefixSHA256: hex.EncodeToString(prefixHash[:]), EffectivePayloadSHA256: hex.EncodeToString(payloadHash[:]), Attachments: chips}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (r Record) Matches(effectivePayload string) bool {
	if validateRecord(r) != nil || r.PrefixBytes > len(effectivePayload) || !utf8.ValidString(effectivePayload) {
		return false
	}
	if r.PrefixBytes > 0 && r.PrefixBytes < len(effectivePayload) && !utf8.RuneStart(effectivePayload[r.PrefixBytes]) {
		return false
	}
	prefix := sha256.Sum256([]byte(effectivePayload[:r.PrefixBytes]))
	payload := sha256.Sum256([]byte(effectivePayload))
	return hex.EncodeToString(prefix[:]) == r.PromptPrefixSHA256 && hex.EncodeToString(payload[:]) == r.EffectivePayloadSHA256
}

// SessionDir returns the managed metadata directory for a session without
// placing caller-controlled session IDs into filesystem paths.
func SessionDir(sessionsDir, sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(sessionsDir, "presentation", hex.EncodeToString(sum[:]))
}

func RecordPath(sessionsDir, sessionID, inputID string) string {
	sum := sha256.Sum256([]byte(inputID))
	return filepath.Join(SessionDir(sessionsDir, sessionID), hex.EncodeToString(sum[:])+".json")
}

func Save(sessionsDir, sessionID, inputID string, record Record) error {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(inputID) == "" {
		return errors.New("session and input IDs are required")
	}
	if err := validateRecord(record); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode input presentation: %w", err)
	}
	if len(data) > maxRecordBytes {
		return fmt.Errorf("input presentation exceeds %d bytes", maxRecordBytes)
	}
	dir := SessionDir(sessionsDir, sessionID)
	if err := securePresentationDir(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := securePresentationDir(dir); err != nil {
		return err
	}
	target := RecordPath(sessionsDir, sessionID, inputID)
	if prior, err := Load(sessionsDir, sessionID, inputID); err == nil {
		if prior.EffectivePayloadSHA256 == record.EffectivePayloadSHA256 && prior.PromptPrefixSHA256 == record.PromptPrefixSHA256 && prior.PrefixBytes == record.PrefixBytes && reflect.DeepEqual(prior.Attachments, record.Attachments) {
			return nil
		}
		return errors.New("input presentation already exists with different prompt metadata")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".presentation-*.tmp")
	if err != nil {
		return fmt.Errorf("create input presentation: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure input presentation: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write input presentation: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync input presentation: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close input presentation: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("publish input presentation: %w", err)
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open input presentation directory for sync: %w", err)
	}
	syncErr := dirFile.Sync()
	closeErr := dirFile.Close()
	if syncErr != nil {
		return fmt.Errorf("sync input presentation directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close input presentation directory: %w", closeErr)
	}
	return nil
}

func Load(sessionsDir, sessionID, inputID string) (Record, error) {
	path := RecordPath(sessionsDir, sessionID, inputID)
	for _, dir := range []string{filepath.Join(sessionsDir, "presentation"), filepath.Dir(path)} {
		dirInfo, err := os.Lstat(dir)
		if err != nil {
			return Record{}, err
		}
		if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() || dirInfo.Mode().Perm()&0o077 != 0 {
			return Record{}, errors.New("input presentation directory is not private")
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Record{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Record{}, errors.New("input presentation is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Record{}, errors.New("input presentation file is not private")
	}
	if info.Size() > maxRecordBytes {
		return Record{}, fmt.Errorf("input presentation exceeds %d bytes", maxRecordBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return Record{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return Record{}, err
	}
	if len(data) > maxRecordBytes {
		return Record{}, fmt.Errorf("input presentation exceeds %d bytes", maxRecordBytes)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, fmt.Errorf("decode input presentation: %w", err)
	}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func validateRecord(record Record) error {
	if record.Version != Version || record.PrefixBytes < 0 || record.PrefixBytes > 1<<20 {
		return errors.New("unsupported or invalid input presentation metadata")
	}
	for _, digest := range []string{record.PromptPrefixSHA256, record.EffectivePayloadSHA256} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("invalid input presentation prompt hash")
		}
	}
	if len(record.Attachments) == 0 || len(record.Attachments) > MaxAttachments {
		return fmt.Errorf("input presentation attachment count must be between 1 and %d", MaxAttachments)
	}
	for _, chip := range record.Attachments {
		if err := validateChip(chip); err != nil {
			return err
		}
	}
	return nil
}

func validateChip(chip Attachment) error {
	if chip.Name == "" || len(chip.Name) > MaxNameBytes || !utf8.ValidString(chip.Name) || strings.ContainsAny(chip.Name, "/\\") {
		return errors.New("invalid input presentation filename")
	}
	for _, r := range chip.Name {
		if unicode.IsControl(r) {
			return errors.New("invalid control character in input presentation filename")
		}
	}
	if chip.Kind != string(attachments.Text) && chip.Kind != string(attachments.Image) && chip.Kind != string(attachments.PDFText) {
		return errors.New("invalid input presentation attachment kind")
	}
	if len(chip.ContentType) > MaxTypeBytes || !utf8.ValidString(chip.ContentType) || strings.ContainsAny(chip.ContentType, "\r\n") {
		return errors.New("invalid input presentation content type")
	}
	if chip.PagesExtracted < 0 || chip.PagesTotal < 0 || chip.PagesExtracted > chip.PagesTotal || chip.PagesTotal > 10000 {
		return errors.New("invalid input presentation page metadata")
	}
	return nil
}

func securePresentationDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create input presentation directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("input presentation path is not a real directory")
	}
	return os.Chmod(dir, 0o700)
}

func boundedUTF8(value string, maxBytes int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= maxBytes {
		return value
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
