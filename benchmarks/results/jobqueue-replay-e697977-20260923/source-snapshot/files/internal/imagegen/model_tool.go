package imagegen

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const (
	ToolName                                   = "ImageGen"
	planType    operation.RemoteJobPlanType    = "pk.imagegen"
	planVersion operation.RemoteJobPlanVersion = 1
)

type toolArguments struct {
	Prompt         string   `json:"prompt"`
	OutputPath     string   `json:"output_path,omitempty"`
	ReferencePaths []string `json:"reference_paths,omitempty"`
}

// Decorator adds the ImageGen tool when a driver model is explicitly configured.
// The driver remains separate from the pk model and effort.
func Decorator(cfg Config, workspace string) func(tool.Registry) tool.Registry {
	return func(base tool.Registry) tool.Registry {
		driver, err := cfg.driverModel()
		if base == nil || err != nil || strings.TrimSpace(driver) == "" {
			return base
		}
		if _, exists := base.Resolve(ToolName); exists {
			return base
		}
		root, err := filepath.Abs(workspace)
		if err != nil {
			return base
		}
		return &imageRegistry{base: base, root: root, config: cfg}
	}
}

// HandlerFactory supplies the run-scoped asynchronous image-generation worker
// required by Decorator. The runner's operation manager cancels it during hard
// stop and waits for its process group to exit during teardown.
func HandlerFactory(cfg Config, workspace string) func(context.Context) []operation.RemoteJobHandler {
	return func(ctx context.Context) []operation.RemoteJobHandler {
		return []operation.RemoteJobHandler{newHandler(ctx, cfg, workspace)}
	}
}

type imageRegistry struct {
	base   tool.Registry
	root   string
	config Config
}

func (r *imageRegistry) StaticDefinitions() []tool.Definition {
	definitions := append([]tool.Definition(nil), r.base.StaticDefinitions()...)
	definitions = append(definitions, tool.Definition{Tool: llm.Tool{
		Type:        llm.ToolFunction,
		Name:        ToolName,
		Description: "Generate one PNG image using the separately configured Codex image worker. Returns the artifact path and image dimensions.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt":          map[string]any{"type": "string", "minLength": 1, "maxLength": maxPromptBytes},
				"output_path":     map[string]any{"type": "string", "description": "Optional new .png path relative to the task workspace."},
				"reference_paths": map[string]any{"type": "array", "maxItems": 5, "items": map[string]any{"type": "string"}, "description": "Optional workspace-relative image files to attach as references."},
			},
			"required":             []string{"prompt"},
			"additionalProperties": false,
		},
	}})
	return definitions
}

func (r *imageRegistry) Resolve(name string) (tool.Translator, bool) {
	if name == ToolName {
		return imageTranslator{root: r.root, config: r.config}, true
	}
	return r.base.Resolve(name)
}

func (r *imageRegistry) RegisterSkill(skill tool.Skill) (tool.RegistrationID, error) {
	return r.base.RegisterSkill(skill)
}
func (r *imageRegistry) UnregisterSkill(id tool.RegistrationID) { r.base.UnregisterSkill(id) }
func (r *imageRegistry) Skills() []tool.Skill                   { return r.base.Skills() }

type imageTranslator struct {
	root   string
	config Config
}

func (t imageTranslator) Translate(callContext tool.Context, call llm.ToolCall) tool.CallStatus {
	if callContext == nil {
		return tool.CallStatus{Error: "image generation context is unavailable"}
	}
	if len(call.Arguments) > 32<<10 {
		return tool.CallStatus{Error: "ImageGen arguments exceed the 32 KiB limit"}
	}
	var args toolArguments
	decoder := json.NewDecoder(strings.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return tool.CallStatus{Error: "invalid ImageGen arguments: " + err.Error()}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return tool.CallStatus{Error: "invalid ImageGen arguments: expected one JSON object"}
	}
	if strings.TrimSpace(args.Prompt) == "" || len(args.Prompt) > maxPromptBytes {
		return tool.CallStatus{Error: "ImageGen prompt must be nonblank and no longer than 16 KiB"}
	}
	if len(args.ReferencePaths) > 5 {
		return tool.CallStatus{Error: "ImageGen accepts at most five reference images"}
	}
	refs, err := workspaceReferences(t.root, args.ReferencePaths)
	if err != nil {
		return tool.CallStatus{Error: err.Error()}
	}
	if args.OutputPath == "" {
		name, err := randomArtifactName()
		if err != nil {
			return tool.CallStatus{Error: "create image artifact name: " + err.Error()}
		}
		args.OutputPath = filepath.ToSlash(filepath.Join("generated_images", name+".png"))
	}
	if _, err := safeNewOutput(t.root, filepath.FromSlash(args.OutputPath)); err != nil {
		return tool.CallStatus{Error: "invalid ImageGen output path: " + err.Error()}
	}
	args.ReferencePaths = refs
	encoded, err := json.Marshal(args)
	if err != nil {
		return tool.CallStatus{Error: "encode ImageGen request: " + err.Error()}
	}
	plan, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: planType, Version: planVersion, Data: jsontext.Value(encoded)})
	if err != nil {
		return tool.CallStatus{Error: "create ImageGen operation: " + err.Error()}
	}
	return tool.CallStatus{WaitingFor: []operation.ID{callContext.Submit(plan)}}
}

func (t imageTranslator) TranslateResult(callID string, status tool.CallStatus, operations []operation.Operation) (llm.ToolResult, error) {
	if status.Error != "" {
		if len(operations) != 0 {
			return llm.ToolResult{}, fmt.Errorf("ImageGen tool call %q has both a validation error and operations", callID)
		}
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: status.Error}}}, nil
	}
	if len(operations) != 1 {
		return llm.ToolResult{CallID: callID}, fmt.Errorf("ImageGen expected one terminal result, got %d", len(operations))
	}
	state, err := operation.DecodeRemoteJobState(operations[0])
	if err != nil {
		return llm.ToolResult{CallID: callID}, err
	}
	var text string
	switch operations[0].Status {
	case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
		text = "Image generation is still running."
	case operation.StatusCompleted:
		text = state.TerminalResult
		if text == "" {
			return llm.ToolResult{CallID: callID}, errors.New("completed ImageGen operation has no artifact result")
		}
	case operation.StatusFailed, operation.StatusCanceled:
		text = state.TerminalError
		if text == "" {
			text = "Image generation was " + string(operations[0].Status) + "."
		}
	default:
		return llm.ToolResult{CallID: callID}, fmt.Errorf("ImageGen operation %q has invalid status %q", operations[0].ID, operations[0].Status)
	}
	return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}, nil
}

func workspaceReferences(root string, paths []string) ([]string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace for image references: %w", err)
	}
	refs := make([]string, 0, len(paths))
	for _, raw := range paths {
		if raw == "" || filepath.IsAbs(raw) {
			return nil, errors.New("reference paths must be relative to the task workspace")
		}
		clean := filepath.Clean(filepath.FromSlash(raw))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, errors.New("reference path escapes the task workspace")
		}
		candidate, err := filepath.EvalSymlinks(filepath.Join(root, clean))
		if err != nil {
			return nil, fmt.Errorf("resolve reference image %q: %w", raw, err)
		}
		rel, err := filepath.Rel(root, candidate)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, errors.New("reference path escapes the task workspace through a symlink")
		}
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("reference image %q is not a regular file", raw)
		}
		refs = append(refs, candidate)
	}
	return refs, nil
}

func randomArtifactName() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

type workerJob struct {
	operation operation.Operation
	cancel    context.CancelFunc
	canceled  bool
}

type handler struct {
	ctx       context.Context
	config    Config
	workspace string
	mu        sync.Mutex
	jobs      map[operation.ID]*workerJob
	updates   chan operation.Operation
	wg        sync.WaitGroup
	done      chan struct{}
}

func newHandler(ctx context.Context, cfg Config, workspace string) *handler {
	h := &handler{ctx: ctx, config: cfg, workspace: workspace, jobs: make(map[operation.ID]*workerJob), updates: make(chan operation.Operation, 64), done: make(chan struct{})}
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		for _, job := range h.jobs {
			job.cancel()
		}
		h.mu.Unlock()
		h.wg.Wait()
		close(h.updates)
		close(h.done)
	}()
	return h
}

func (h *handler) RemoteJobPlanType() operation.RemoteJobPlanType       { return planType }
func (h *handler) RemoteJobPlanVersion() operation.RemoteJobPlanVersion { return planVersion }
func (h *handler) RemoteJobUpdates() <-chan operation.Operation         { return h.updates }
func (h *handler) Wait()                                                { <-h.done }

func (h *handler) AddRemoteJob(op operation.Operation) error {
	state, err := operation.DecodeRemoteJobState(op)
	if err != nil {
		return err
	}
	if state.Plan.Type != planType || state.Plan.Version != planVersion {
		return errors.New("unsupported ImageGen operation plan")
	}
	var args toolArguments
	if err := json.Unmarshal(state.Plan.Data, &args); err != nil {
		return fmt.Errorf("decode ImageGen request: %w", err)
	}
	if strings.TrimSpace(args.Prompt) == "" || len(args.Prompt) > maxPromptBytes || len(args.ReferencePaths) > 5 {
		return errors.New("invalid ImageGen operation plan limits")
	}
	refs, err := workspaceReferences(h.workspace, args.ReferencePaths)
	if err != nil {
		return err
	}
	args.ReferencePaths = refs
	if _, err := safeNewOutput(h.workspace, filepath.FromSlash(args.OutputPath)); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil {
		return h.ctx.Err()
	}
	if _, exists := h.jobs[op.ID]; exists {
		return nil
	}
	jobCtx, cancel := context.WithCancel(h.ctx)
	job := &workerJob{operation: op, cancel: cancel}
	h.jobs[op.ID] = job
	h.wg.Add(1)
	go h.execute(jobCtx, op.ID, job, args)
	return nil
}

func (h *handler) CancelRemoteJob(id operation.ID, _ string) error {
	h.mu.Lock()
	job := h.jobs[id]
	if job != nil {
		job.canceled = true
		job.cancel()
	}
	h.mu.Unlock()
	return nil
}

func (h *handler) execute(ctx context.Context, id operation.ID, job *workerJob, args toolArguments) {
	defer h.wg.Done()
	result, runErr := Generate(ctx, h.config, Request{Prompt: args.Prompt, OutputRoot: h.workspace, OutputPath: filepath.FromSlash(args.OutputPath), References: args.ReferencePaths})
	h.mu.Lock()
	canceled := job.canceled || ctx.Err() != nil
	delete(h.jobs, id)
	h.mu.Unlock()
	var step operation.Step
	var err error
	if canceled {
		step, err = operation.CancelRemoteJob(job.operation)
	} else if runErr != nil {
		step, err = operation.FailRemoteJob(job.operation, runErr)
	} else {
		payload, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			step, err = operation.FailRemoteJob(job.operation, marshalErr)
		} else {
			state, decodeErr := operation.DecodeRemoteJobState(job.operation)
			if decodeErr != nil {
				step, err = operation.FailRemoteJob(job.operation, decodeErr)
			} else {
				state.TerminalResult = string(payload)
				step, err = operation.UpdateRemoteJob(job.operation, state, operation.StatusCompleted)
			}
		}
	}
	if err != nil || step.Operation == nil {
		return
	}
	select {
	case h.updates <- *step.Operation:
	case <-h.ctx.Done():
	}
}
