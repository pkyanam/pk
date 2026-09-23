package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/acp"
	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestACPInlineImageUsesDurableViewImageRunnerPathAndReplays(t *testing.T) {
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	sessionID, err := runner.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	inputID := "acp-input-test"
	imageBytes := tinyACPImage(t)
	blocks := []acp.PromptBlock{{Type: "text", Text: "describe this"}, {Type: "image", MIMEType: "image/png", ImageData: imageBytes}, {Type: "text", Text: "then summarize"}}
	turn := acp.Turn{SessionID: sessionID, Blocks: blocks}
	prompt, before, after, cleanup, err := prepareACPImageInput(sessionsDir, turn, inputID)
	if err != nil {
		t.Fatal(err)
	}
	dir := attachments.PromptImageDir(sessionsDir, sessionID, inputID)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("artifact exists before persistence hook: %v", err)
	}
	imagePath := filepath.Join(dir, "image-01.png")
	callArgs, _ := json.Marshal(map[string]string{"path": imagePath})
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "view-image-call", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "view-image", Name: "ViewImage", Arguments: string(callArgs)}}}}},
		{response: llm.Response{ID: "image-answer", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "The image is a test pixel."}}}}},
	}}
	var output bytes.Buffer
	persisted := false
	result, err := runner.Run(context.Background(), runner.Options{
		Prompt: prompt, PromptID: inputID, PreallocatedNewID: sessionID,
		Workspace: t.TempDir(), SessionDir: sessionsDir, Model: "gpt-6-luna", Effort: "medium",
		Adapter: model, Output: &output, JSONL: true, ToolEvents: true,
		BeforeInputPersist: before,
		AfterInputPersist:  func(id, input string) { persisted = true; after(id, input) },
	})
	if err != nil {
		t.Fatalf("runner did not complete ViewImage flow: %v\n%s", err, output.String())
	}
	if result.SessionID != sessionID || !persisted {
		t.Fatalf("run result=%+v persisted=%v", result, persisted)
	}
	info, err := os.Stat(imagePath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("durable image artifact mode=%v err=%v", info, err)
	}
	if !strings.Contains(output.String(), "tool_call") || model.calls != 2 {
		t.Fatalf("ViewImage runner path not exercised: calls=%d output=%s", model.calls, output.String())
	}
	content, found, err := acpSavedUserContent(sessionsDir, sessionID, inputID, prompt)
	if err != nil || !found || len(content) != 3 {
		t.Fatalf("replayed content found=%v len=%d err=%v", found, len(content), err)
	}
	if content[0].(map[string]any)["text"] != "describe this" || content[2].(map[string]any)["text"] != "then summarize" {
		t.Fatalf("replay order=%v", content)
	}
	imageContent := content[1].(map[string]any)
	decoded, err := base64.StdEncoding.DecodeString(imageContent["data"].(string))
	if err != nil || !bytes.Equal(decoded, imageBytes) || imageContent["mimeType"] != "image/png" {
		t.Fatalf("replayed image content=%v err=%v", imageContent, err)
	}
	if _, _, err := acpSavedUserContent(sessionsDir, sessionID, inputID, prompt+"tampered"); err == nil {
		t.Fatal("replay accepted a sidecar bound to a different prompt")
	}
	cleanup() // test cleanup is deliberately after reading durable replay content
}

func TestACPInlineImageFailuresDoNotLeaveArtifacts(t *testing.T) {
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	base := tinyACPImage(t)
	tooWide := mutatePNGDimensions(t, base, 5000, 4000) // 20M pixels each, below the per-image cap
	blocks := []acp.PromptBlock{{Type: "image", MIMEType: "image/png", ImageData: tooWide}, {Type: "image", MIMEType: "image/png", ImageData: tooWide}}
	turn := acp.Turn{SessionID: "acp-session", Blocks: blocks}
	_, before, _, cleanup, err := prepareACPImageInput(sessionsDir, turn, "input-oversized-pixels")
	if err != nil {
		t.Fatal(err)
	}
	if err := before(turn.SessionID, "input-oversized-pixels"); err == nil || !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("aggregate dimensions error=%v", err)
	}
	dir := attachments.PromptImageDir(sessionsDir, turn.SessionID, "input-oversized-pixels")
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("failed image validation left artifacts: %v", err)
	}
	cleanup()

	validTurn := acp.Turn{SessionID: "acp-session", Blocks: []acp.PromptBlock{{Type: "image", MIMEType: "image/png", ImageData: base}}}
	_, before, _, cleanup, err = prepareACPImageInput(sessionsDir, validTurn, "input-cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if err := before(validTurn.SessionID, "input-cancelled"); err != nil {
		t.Fatal(err)
	}
	cleanup() // runner failure before AfterInputPersist
	if _, err := os.Lstat(attachments.PromptImageDir(sessionsDir, validTurn.SessionID, "input-cancelled")); !os.IsNotExist(err) {
		t.Fatalf("unpersisted input artifacts survived cleanup: %v", err)
	}
}

func tinyACPImage(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func mutatePNGDimensions(t *testing.T, data []byte, width, height uint32) []byte {
	t.Helper()
	if len(data) < 33 {
		t.Fatal("test PNG too short")
	}
	result := append([]byte(nil), data...)
	binary.BigEndian.PutUint32(result[16:20], width)
	binary.BigEndian.PutUint32(result[20:24], height)
	binary.BigEndian.PutUint32(result[29:33], crc32.ChecksumIEEE(result[12:29]))
	return result
}
