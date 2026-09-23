// Package subagents manages bounded, same-workspace child runner sessions.
// File ownership is coordination guidance; children retain the parent's tools
// and permissions and are not a security sandbox.
package subagents

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pkyanam/pk/internal/runner"
)

const (
	defaultMaxConcurrent = 2
	maxTaskBytes         = 16 << 10
	maxFiles             = 64
	maxQueuedChildren    = 8
	maxFileBytes         = 4 << 10
	maxEventText         = 8 << 10
	maxReportBytes       = 32 << 10
)

type RunnerFactory func(context.Context, runner.Options) (runner.RunResult, error)

type Config struct {
	Workspace, SessionDir, Model, Effort, SystemPrompt string
	SkillsDirs                                         []string
	MaxConcurrent                                      int
	Depth                                              int
	Runner                                             RunnerFactory
	// Events callbacks may inspect or mutate manager state and may call emit
	// indirectly, but must not call Manager.Close; Close drains callback delivery.
	Events func(Event)
}

type LaunchRequest struct {
	Task        string
	Files       []string
	TaskOnly    bool
	Model       string
	Effort      string
	ParentDepth int
	RequestID   string
}

type Child struct {
	ID, RequestID, State, Task, SessionID, Model, Effort string
	Files                                                []string
	TaskOnly                                             bool
	StartedAt, UpdatedAt                                 time.Time
	Error                                                string
}

type Report struct {
	Child      Child
	Text       string
	StartedAt  time.Time
	FinishedAt time.Time
	Error      string
}

type Event struct {
	Type      string          `json:"type"`
	ChildID   string          `json:"child_id"`
	RequestID string          `json:"request_id,omitempty"`
	Sequence  uint64          `json:"sequence"`
	At        time.Time       `json:"at"`
	State     string          `json:"state,omitempty"`
	Text      string          `json:"text,omitempty"`
	Phase     string          `json:"phase,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	ToolName  string          `json:"name,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type Manager struct {
	cfg       Config
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.RWMutex
	children  map[string]*child
	closed    bool
	sequence  uint64
	eventMu   sync.Mutex
	events    []Event
	emitting  bool
	eventDone chan struct{}
	active    int
	queue     []*child
}

type depthContextKey struct{}

// DepthFromContext reports the subagent nesting level injected for child runner
// factories. Factories should omit parent-only helper tools when this is > 0.
func DepthFromContext(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	depth, _ := ctx.Value(depthContextKey{}).(int)
	return depth
}

type child struct {
	info        Child
	claims      []string
	ctx         context.Context
	cancel      context.CancelFunc
	inputs      chan runner.Input
	inputMu     sync.Mutex
	inputClosed bool
	done        chan struct{}
	final       chan struct{}
	finalOnce   sync.Once
	doneOnce    sync.Once
	report      Report
	textMu      sync.Mutex
	text        strings.Builder
}

func (c *child) closeDone() { c.doneOnce.Do(func() { close(c.done) }) }

func (c *child) closeInputs() {
	c.inputMu.Lock()
	if !c.inputClosed {
		c.inputClosed = true
		close(c.inputs)
	}
	c.inputMu.Unlock()
}

func New(cfg Config) (*Manager, error) {
	if cfg.Runner == nil {
		return nil, errors.New("subagent runner factory is required")
	}
	if strings.TrimSpace(cfg.Workspace) == "" {
		return nil, errors.New("subagent workspace is required")
	}
	workspace, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve subagent workspace: %w", err)
	}
	cfg.Workspace = workspace
	if cfg.Model == "" {
		cfg.Model = "gpt-6-luna"
	}
	if cfg.Effort == "" {
		cfg.Effort = "medium"
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = defaultMaxConcurrent
	}
	if cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 8 {
		return nil, errors.New("subagent max concurrency must be between 1 and 8")
	}
	if cfg.Depth < 0 {
		return nil, errors.New("subagent depth cannot be negative")
	}
	managerCtx, managerCancel := context.WithCancel(context.Background())
	return &Manager{cfg: cfg, ctx: managerCtx, cancel: managerCancel, children: make(map[string]*child)}, nil
}

func (m *Manager) Launch(ctx context.Context, req LaunchRequest) (Child, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m.cfg.Depth > 0 || req.ParentDepth > 0 || DepthFromContext(ctx) > 0 {
		return Child{}, errors.New("nested subagents are disabled")
	}
	task := strings.TrimSpace(req.Task)
	if task == "" || len(task) > maxTaskBytes {
		return Child{}, errors.New("subagent task must be non-empty and at most 16 KiB")
	}
	if req.TaskOnly && len(req.Files) > 0 {
		return Child{}, errors.New("task-only subagent requests cannot also declare owned files")
	}
	if !req.TaskOnly && (len(req.Files) == 0 || len(req.Files) > maxFiles) {
		return Child{}, errors.New("subagent must declare between 1 and 64 owned files, or set task_only=true with no files")
	}
	files := make([]string, 0, len(req.Files))
	claims := make([]string, 0, len(req.Files))
	seen := map[string]bool{}
	for _, f := range req.Files {
		f = filepath.Clean(strings.TrimSpace(f))
		if f == "" || f == "." || filepath.IsAbs(f) || f == ".." || strings.HasPrefix(f, ".."+string(filepath.Separator)) || len(f) > maxFileBytes {
			return Child{}, fmt.Errorf("invalid owned file path %q", f)
		}
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
			claim, err := canonicalClaim(m.cfg.Workspace, f)
			if err != nil {
				return Child{}, fmt.Errorf("resolve owned file path %q: %w", f, err)
			}
			claims = append(claims, claim)
		}
	}
	if !req.TaskOnly && len(files) == 0 {
		return Child{}, errors.New("subagent must declare at least one owned file unless task_only=true")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Child{}, errors.New("subagent manager is closed")
	}
	for _, c := range m.children {
		if c.info.State != "running" && c.info.State != "queued" {
			continue
		}
		for _, owned := range claims {
			for _, existing := range c.claims {
				if pathsOverlap(owned, existing) {
					m.mu.Unlock()
					return Child{}, fmt.Errorf("owned path %q conflicts with child %s path %q (%s)", owned, c.info.ID, existing, c.info.State)
				}
			}
		}
	}
	immediate := m.active < m.cfg.MaxConcurrent && len(m.queue) == 0
	if !immediate && len(m.queue) >= maxQueuedChildren {
		m.mu.Unlock()
		return Child{}, fmt.Errorf("subagent queue is full (%d active, %d queued maximum)", m.active, maxQueuedChildren)
	}
	id, err := randomID()
	if err != nil {
		m.mu.Unlock()
		return Child{}, err
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return Child{}, err
	}
	childCtx, cancel := context.WithCancel(context.WithValue(m.ctx, depthContextKey{}, m.cfg.Depth+1))
	now := time.Now().UTC()
	state, eventText := "running", "Child started"
	if !immediate {
		state, eventText = "queued", "Child queued; at most 8 children wait for a concurrency slot"
	}
	c := &child{info: Child{ID: id, RequestID: req.RequestID, State: state, Task: task, Files: files, TaskOnly: req.TaskOnly, Model: choose(req.Model, m.cfg.Model), Effort: choose(req.Effort, m.cfg.Effort), StartedAt: now, UpdatedAt: now}, claims: claims, ctx: childCtx, cancel: cancel, inputs: make(chan runner.Input, 64), done: make(chan struct{}), final: make(chan struct{})}
	m.children[id] = c
	if immediate {
		m.active++
	} else {
		m.queue = append(m.queue, c)
	}
	initial := c.info
	initial.Files = append([]string(nil), c.info.Files...)
	m.mu.Unlock()
	m.emit(Event{Type: "subagent", ChildID: id, RequestID: req.RequestID, State: state, Text: eventText})
	if immediate {
		go m.run(c)
	}
	return initial, nil
}

func (m *Manager) run(c *child) {
	defer c.cancel()
	defer c.closeDone()
	defer m.releaseActive()
	if c.ctx.Err() != nil {
		m.finishCanceledBeforeStart(c)
		return
	}
	r, w := io.Pipe()
	readDone := make(chan struct{})
	go m.readEvents(c, r, readDone)
	prompt := buildChildPrompt(c.info.Task, c.info.Files, c.info.TaskOnly)
	opts := runner.Options{Prompt: prompt, PromptID: "subagent-" + c.info.ID, Workspace: m.cfg.Workspace, SessionDir: m.cfg.SessionDir, Model: c.info.Model, Effort: c.info.Effort, SystemPrompt: m.cfg.SystemPrompt, SkillsDirs: append([]string(nil), m.cfg.SkillsDirs...), JSONL: true, ToolEvents: true, Output: w, OnSession: func(id string) {
		m.mu.Lock()
		c.info.SessionID = id
		c.info.UpdatedAt = time.Now().UTC()
		m.mu.Unlock()
		m.emit(Event{Type: "subagent", ChildID: c.info.ID, RequestID: c.info.RequestID, State: "running", Text: "session=" + id})
	}, Inputs: c.inputs, QueueInputs: true, KeepAlive: false, CaptureLimit: maxReportBytes}
	result, err := m.cfg.Runner(c.ctx, opts)
	_ = w.Close()
	<-readDone
	if result.SessionID != "" {
		m.mu.Lock()
		c.info.SessionID = result.SessionID
		m.mu.Unlock()
	}
	if result.Text != "" {
		c.textMu.Lock()
		c.text.Reset()
		_, _ = c.text.WriteString(limit(result.Text, maxReportBytes))
		c.textMu.Unlock()
	}
	m.mu.Lock()
	if c.ctx.Err() != nil {
		c.info.State = "canceled"
		c.info.Error = c.ctx.Err().Error()
	} else if err != nil {
		c.info.State = "failed"
		c.info.Error = err.Error()
	} else {
		c.info.State = "completed"
	}
	c.info.UpdatedAt = time.Now().UTC()
	c.textMu.Lock()
	text := c.text.String()
	c.textMu.Unlock()
	c.report = Report{Child: c.info, Text: text, StartedAt: c.info.StartedAt, FinishedAt: c.info.UpdatedAt, Error: c.info.Error}
	m.mu.Unlock()
	m.emit(Event{Type: "subagent", ChildID: c.info.ID, RequestID: c.info.RequestID, State: c.info.State, Text: limit(c.info.Error, 1024)})
	c.finalOnce.Do(func() { close(c.final) })
}

func (m *Manager) finishCanceledBeforeStart(c *child) {
	now := time.Now().UTC()
	m.mu.Lock()
	c.info.State = "canceled"
	c.info.Error = "canceled before a concurrency slot was available"
	c.info.UpdatedAt = now
	c.report = Report{Child: c.info, StartedAt: c.info.StartedAt, FinishedAt: now, Error: c.info.Error}
	m.mu.Unlock()
	m.emit(Event{Type: "subagent", ChildID: c.info.ID, RequestID: c.info.RequestID, State: "canceled", Text: c.info.Error})
	c.finalOnce.Do(func() { close(c.final) })
}

func (m *Manager) releaseActive() {
	var start []*child
	var canceled []*child
	m.mu.Lock()
	if m.active > 0 {
		m.active--
	}
	for m.active < m.cfg.MaxConcurrent && len(m.queue) > 0 {
		next := m.queue[0]
		m.queue = m.queue[1:]
		if next.ctx.Err() != nil || m.closed {
			m.markQueuedCanceledLocked(next, "canceled before a concurrency slot was available")
			canceled = append(canceled, next)
			continue
		}
		next.info.State = "running"
		next.info.UpdatedAt = time.Now().UTC()
		m.active++
		start = append(start, next)
	}
	m.mu.Unlock()
	for _, c := range canceled {
		m.completeQueuedCanceled(c)
	}
	for _, c := range start {
		m.emit(Event{Type: "subagent", ChildID: c.info.ID, RequestID: c.info.RequestID, State: "running", Text: "Child started after waiting for a concurrency slot"})
		go m.run(c)
	}
}

func (m *Manager) markQueuedCanceledLocked(c *child, reason string) {
	c.info.State = "canceled"
	c.info.Error = reason
	c.info.UpdatedAt = time.Now().UTC()
	c.report = Report{Child: c.info, StartedAt: c.info.StartedAt, FinishedAt: c.info.UpdatedAt, Error: reason}
}

func (m *Manager) completeQueuedCanceled(c *child) {
	c.cancel()
	c.closeInputs()
	m.emit(Event{Type: "subagent", ChildID: c.info.ID, RequestID: c.info.RequestID, State: "canceled", Text: c.info.Error})
	c.finalOnce.Do(func() { close(c.final) })
	c.closeDone()
}

func pathsOverlap(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		left, right = strings.ToLower(left), strings.ToLower(right)
	}
	if left == right {
		return true
	}
	separator := string(filepath.Separator)
	return strings.HasPrefix(left, right+separator) || strings.HasPrefix(right, left+separator)
}

// canonicalClaim resolves existing symlink components so advisory ownership
// claims for aliases of the same workspace path conflict. Missing leaf paths
// are supported by resolving the nearest existing parent.
func canonicalClaim(workspace, relative string) (string, error) {
	path := filepath.Join(workspace, relative)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Resolve the longest existing parent and retain a missing suffix.
		parent := filepath.Dir(path)
		suffix := []string{filepath.Base(path)}
		found := false
		for {
			r, parentErr := filepath.EvalSymlinks(parent)
			if parentErr == nil {
				parts := append([]string{r}, suffix...)
				resolved = filepath.Join(parts...)
				found = true
				break
			}
			if parent == filepath.Dir(parent) {
				break
			}
			suffix = append([]string{filepath.Base(parent)}, suffix...)
			parent = filepath.Dir(parent)
		}
		if !found {
			return "", err
		}
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		resolved = strings.ToLower(resolved)
	}
	return filepath.Clean(resolved), nil
}

func (m *Manager) readEvents(c *child, r *io.PipeReader, done chan<- struct{}) {
	defer close(done)
	defer r.Close()
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 1<<20)
	for s.Scan() {
		var raw map[string]json.RawMessage
		if json.Unmarshal(s.Bytes(), &raw) != nil {
			continue
		}
		var typ string
		_ = json.Unmarshal(raw["type"], &typ)
		e := Event{Type: typ, ChildID: c.info.ID, RequestID: c.info.RequestID, At: time.Now().UTC()}
		switch typ {
		case "assistant":
			_ = json.Unmarshal(raw["phase"], &e.Phase)
			_ = json.Unmarshal(raw["text"], &e.Text)
			if e.Phase == "final" || e.Phase == "final_answer" {
				c.finalOnce.Do(func() { close(c.final) })
			}
		case "tool_call":
			_ = json.Unmarshal(raw["call_id"], &e.CallID)
			_ = json.Unmarshal(raw["name"], &e.ToolName)
			_ = json.Unmarshal(raw["state"], &e.State)
			e.Payload = boundedRaw(raw, 6<<10)
		case "usage", "session", "model":
			e.Payload = boundedRaw(raw, 4<<10)
		default:
			continue
		}
		e.Text = limit(e.Text, maxEventText)
		if typ == "assistant" && e.Text != "" {
			c.textMu.Lock()
			if c.text.Len() < maxReportBytes {
				_, _ = c.text.WriteString(limit(e.Text, maxReportBytes-c.text.Len()))
				c.text.WriteByte('\n')
			}
			c.textMu.Unlock()
		}
		m.emit(e)
	}
}

func (m *Manager) emit(e Event) {
	if m.cfg.Events == nil {
		return
	}
	// Queue events under a dedicated lock, then invoke callbacks with no lock
	// held. Reentrant callbacks can append events, which this dispatcher drains
	// after the current callback returns.
	m.eventMu.Lock()
	m.sequence++
	e.Sequence = m.sequence
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	m.events = append(m.events, e)
	if m.emitting {
		m.eventMu.Unlock()
		return
	}
	m.emitting = true
	m.eventDone = make(chan struct{})
	m.eventMu.Unlock()
	for {
		m.eventMu.Lock()
		if len(m.events) == 0 {
			m.emitting = false
			close(m.eventDone)
			m.eventDone = nil
			m.eventMu.Unlock()
			return
		}
		queued := m.events[0]
		m.events[0] = Event{}
		m.events = m.events[1:]
		m.eventMu.Unlock()
		m.cfg.Events(queued)
	}
}

// waitEvents waits until the current event batch has been delivered. Callers
// must invoke it outside cfg.Events: waiting from inside the dispatcher callback
// would wait for that callback to return.
func (m *Manager) waitEvents() {
	for {
		m.eventMu.Lock()
		done := m.eventDone
		m.eventMu.Unlock()
		if done == nil {
			return
		}
		<-done
	}
}

func (m *Manager) Status(id string) (Child, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.children[id]
	if !ok {
		return Child{}, false
	}
	out := c.info
	out.Files = append([]string(nil), out.Files...)
	return out, true
}

func (m *Manager) SendInput(ctx context.Context, id, text string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("subagent input must not be empty")
	}
	if len(text) > maxTaskBytes {
		return errors.New("subagent input exceeds 16 KiB")
	}
	m.mu.RLock()
	c := m.children[id]
	m.mu.RUnlock()
	if c == nil {
		return errors.New("unknown subagent")
	}
	ack := make(chan error, 1)
	c.inputMu.Lock()
	if c.inputClosed {
		c.inputMu.Unlock()
		return errors.New("subagent no longer accepts input")
	}
	input := runner.Input{ID: "steer-" + mustID(), Text: text, Accepted: func(err error) { ack <- err }}
	select {
	case c.inputs <- input:
		c.inputMu.Unlock()
	case <-ctx.Done():
		c.inputMu.Unlock()
		return ctx.Err()
	case <-c.ctx.Done():
		c.inputMu.Unlock()
		return errors.New("subagent stopped")
	}
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		select {
		case err := <-ack:
			return err
		default:
			return errors.New("subagent stopped before input was accepted")
		}
	}
}

func (m *Manager) Wait(ctx context.Context, id string) (Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	c := m.children[id]
	m.mu.RUnlock()
	if c == nil {
		return Report{}, errors.New("unknown subagent")
	}
	select {
	case <-ctx.Done():
		return Report{}, ctx.Err()
	case <-c.done:
		return c.report, nil
	case <-c.final:
	}
	c.inputMu.Lock()
	if !c.inputClosed {
		c.inputClosed = true
		close(c.inputs)
	}
	c.inputMu.Unlock()
	select {
	case <-ctx.Done():
		return Report{}, ctx.Err()
	case <-c.done:
		return c.report, nil
	}
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	c := m.children[id]
	if c == nil {
		m.mu.Unlock()
		return errors.New("unknown subagent")
	}
	if c.info.State == "queued" {
		for index, queued := range m.queue {
			if queued == c {
				m.queue = append(m.queue[:index], m.queue[index+1:]...)
				break
			}
		}
		m.markQueuedCanceledLocked(c, "canceled before a concurrency slot was available")
		m.mu.Unlock()
		m.completeQueuedCanceled(c)
		return nil
	}
	m.mu.Unlock()
	c.cancel()
	c.closeInputs()
	return nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	cs := make([]*child, 0, len(m.children))
	queued := append([]*child(nil), m.queue...)
	m.queue = nil
	for _, c := range queued {
		m.markQueuedCanceledLocked(c, "canceled before a concurrency slot was available")
	}
	for _, c := range m.children {
		cs = append(cs, c)
	}
	m.mu.Unlock()
	m.cancel()
	for _, c := range cs {
		c.cancel()
		c.closeInputs()
	}
	for _, c := range queued {
		m.completeQueuedCanceled(c)
	}
	for _, c := range cs {
		<-c.done
	}
	m.waitEvents()
}

func buildChildPrompt(task string, files []string, taskOnly bool) string {
	if taskOnly {
		return "You are a child coding agent working in the parent's shared writable workspace. This is task-only mode: no files are assigned as exclusive ownership. The parent still has the same workspace tools and permissions; this is not an OS sandbox or read-only mode. Work on the requested investigation or task, coordinate before changing files that may overlap other work, and report any changes and tests. Do not spawn agents or create hidden worktrees. Task: " + task
	}
	b, _ := json.Marshal(files)
	return "You are a child coding agent working in the parent's shared writable workspace. Work only in the declared owned files: " + string(b) + ". This is coordination guidance, not a security sandbox. Do not spawn agents or alter files owned by other agents. Do not create hidden worktrees. Complete and verify the task, then report changed files and tests. Task: " + task
}
func choose(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func mustID() string {
	id, err := randomID()
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return id
}
func limit(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[truncated]"
}
func boundedRaw(raw map[string]json.RawMessage, n int) json.RawMessage {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	if len(b) > n {
		return json.RawMessage(fmt.Sprintf(`{"truncated":true,"bytes":%d}`, len(b)))
	}
	return b
}
