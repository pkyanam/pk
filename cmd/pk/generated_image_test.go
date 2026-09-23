package main

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestGeneratedImageHistoryIsStructuredAndWorkspaceBound(t *testing.T) {
	workspace := t.TempDir()
	imagePath := filepath.Join(workspace, "generated_images", "result.png")
	if err := os.MkdirAll(filepath.Dir(imagePath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestPNG(t, imagePath)
	imageResult, err := json.Marshal(generatedImage{Path: "generated_images/result.png", Width: 3, Height: 2, MIME: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	plan := operation.RemoteJobPlan{Type: "pk.imagegen", Version: 1, Data: jsontext.Value(`{}`)}
	spec, err := operation.NewRemoteJobSpec(plan)
	if err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(operation.RemoteJobState{Plan: plan, TerminalResult: string(imageResult)})
	if err != nil {
		t.Fatal(err)
	}
	current := operation.Operation{ID: "image-op", Type: spec.Type, Version: spec.Version, Status: operation.StatusCompleted, MaxOutputLength: spec.MaxOutputLength, State: jsontext.Value(state)}
	items := []sessionstore.Item{
		{Sequence: 1, Kind: sessionstore.ItemModelResponse, Data: sessionstore.ModelResponse{TurnID: "turn", Response: llmResponseWithImageCall()}},
		{Sequence: 2, Kind: sessionstore.ItemToolCallStatus, Data: sessionstore.ToolCallStatus{TurnID: "turn", CallID: "call-image", Status: tool.CallStatus{WaitingFor: []operation.ID{"image-op"}}, Operations: []operation.Operation{current}}},
	}
	entries, _, _ := projectHistory(items, workspace)
	if len(entries) != 1 || len(entries[0].GeneratedImages) != 1 {
		t.Fatalf("history image metadata missing: %+v", entries)
	}
	if got := entries[0].GeneratedImages[0]; got.Path != "generated_images/result.png" || got.Width != 3 || got.Height != 2 || got.MIME != "image/png" {
		t.Fatalf("unexpected image metadata: %+v", got)
	}
	entries, _, _ = projectHistory(items)
	if len(entries) != 1 || len(entries[0].GeneratedImages) != 0 {
		t.Fatalf("history exposed an unvalidated image without workspace: %+v", entries)
	}
	current.Status = operation.StatusReady
	if _, ok := generatedImageFromOperation(current, workspace); ok {
		t.Fatal("nonterminal image job exposed metadata")
	}
	current.Status = operation.StatusCompleted
	current.State = jsontext.Value(strings.ReplaceAll(string(current.State), "pk.imagegen", "other.remote"))
	if _, ok := generatedImageFromOperation(current, workspace); ok {
		t.Fatal("non-ImageGen operation exposed metadata")
	}
	if _, ok := validateGeneratedImage(generatedImage{Path: "../outside.png", Width: 3, Height: 2, MIME: "image/png"}, workspace); ok {
		t.Fatal("escaping artifact path was accepted")
	}
	abs, err := filepath.Abs(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	if normalized, ok := validateGeneratedImage(generatedImage{Path: abs, Width: 3, Height: 2, MIME: "image/png"}, workspace); !ok || normalized.Path != "generated_images/result.png" {
		t.Fatalf("in-workspace absolute ImageGen path was not normalized: %+v accepted=%v", normalized, ok)
	}
	if _, ok := validateGeneratedImage(generatedImage{Path: "/etc/passwd", Width: 3, Height: 2, MIME: "image/png"}, workspace); ok {
		t.Fatal("out-of-workspace absolute artifact path was accepted")
	}
}

func TestLiveToolEventAddsValidatedGeneratedImages(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "generated_images", "live.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestPNG(t, path)
	var output bytes.Buffer
	server := &rpcServer{output: &output}
	writer := &rpcRunnerOutput{server: server, id: "turn-1", workspace: workspace}
	line := `{"type":"tool_call","name":"ImageGen","state":"completed","operations":[{"id":"op","state":"completed","generated_image":{"path":"generated_images/live.png","width":3,"height":2,"mime":"image/png"}}]}` + "\n"
	if _, err := writer.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	var event rpcEvent
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(event.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		GeneratedImages []generatedImage `json:"generated_images"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || len(payload.GeneratedImages) != 1 {
		t.Fatalf("live generated image missing: payload=%s err=%v", data, err)
	}
	if strings.Contains(string(data), "generated_image\"") {
		t.Fatalf("raw candidate key leaked: %s", data)
	}

	output.Reset()
	writer = &rpcRunnerOutput{server: server, id: "turn-2", workspace: workspace}
	line = `{"type":"tool_call","name":"ImageGen","state":"completed","operations":[{"id":"op","state":"completed","generated_image":{"path":"../escape.png","width":3,"height":2,"mime":"image/png"}}]}` + "\n"
	_, _ = writer.Write([]byte(line))
	if strings.Contains(output.String(), "generated_images") {
		t.Fatalf("unsafe live artifact was emitted: %s", output.String())
	}
}

func llmResponseWithImageCall() llm.Response {
	return llm.Response{Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-image", Name: "ImageGen", Arguments: `{"prompt":"draw"}`}}}}
}

func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{R: 20, G: 200, B: 120, A: 255})
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
