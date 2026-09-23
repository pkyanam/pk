// Package tasks provides durable, detached task execution for the pk CLI.
// A task's manifest and append-only event stream live below a private Store root.
package tasks

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/contextbudget"
	"github.com/pkyanam/pk/internal/runner"
)

const defaultModel = "gpt-6-luna"
const defaultEffort = "medium"

var (
	ErrNotFound       = errors.New("task not found")
	ErrAlreadyRunning = errors.New("task already has a running worker")
	ErrInvalidID      = errors.New("invalid task id")
)

type Status string

const (
	StatusQueued      Status = "queued"
	StatusRunning     Status = "running"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusCanceled    Status = "canceled"
	StatusInterrupted Status = "interrupted"
)

type StartOptions struct {
	ID                      string                          `json:"id,omitempty"`
	PromptID                string                          `json:"prompt_id,omitempty"`
	Prompt                  string                          `json:"prompt"`
	Workspace               string                          `json:"workspace"`
	Model                   string                          `json:"model,omitempty"`
	Effort                  string                          `json:"effort,omitempty"`
	ContextPolicy           string                          `json:"context_policy,omitempty"`
	ProviderID              string                          `json:"provider_id,omitempty"`
	ContextBudget           contextbudget.Budget            `json:"context_budget,omitempty"`
	HistoryCompaction       runner.HistoryCompactionOptions `json:"history_compaction,omitempty"`
	ContextBudgetConfig     config.ContextBudgetConfig      `json:"context_budget_config,omitempty"`
	HistoryCompactionConfig config.HistoryCompactionConfig  `json:"history_compaction_config,omitempty"`
	SystemPrompt            string                          `json:"system_prompt,omitempty"`
	SessionDir              string                          `json:"session_dir,omitempty"`
	SkillsDirs              []string                        `json:"skills_dirs,omitempty"`
	JSONL                   bool                            `json:"jsonl,omitempty"`
	ToolEvents              bool                            `json:"tool_events,omitempty"`
	UseCodex                bool                            `json:"use_codex,omitempty"`
	CodexPath               string                          `json:"codex_path,omitempty"`
	Executable              string                          `json:"executable,omitempty"`
}

// WorkerOptions is the persisted runner input plus runtime-only callbacks.
type WorkerOptions struct {
	StartOptions
	OnSession func(string) `json:"-"`
	Inputs    <-chan Input `json:"-"`
	SessionID string       `json:"-"`
	Resume    bool         `json:"-"`
	KeepAlive bool         `json:"-"`
}

// Input is a durable steering message. Ack must be called only after the
// runner has accepted its stable ID into the restored session inbox.
type Input struct {
	ID   string
	Text string
	Ack  func(error)
}

type Task struct {
	ID              string       `json:"id"`
	Status          Status       `json:"status"`
	Prompt          string       `json:"prompt"`
	Workspace       string       `json:"workspace"`
	Model           string       `json:"model"`
	Effort          string       `json:"effort"`
	ProviderID      string       `json:"provider_id,omitempty"`
	PID             int          `json:"pid,omitempty"`
	SessionID       string       `json:"session_id,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	StartedAt       *time.Time   `json:"started_at,omitempty"`
	FinishedAt      *time.Time   `json:"finished_at,omitempty"`
	Error           string       `json:"error,omitempty"`
	LastEvent       uint64       `json:"last_event"`
	Attempts        int          `json:"attempts"`
	PendingPromptID string       `json:"pending_prompt_id,omitempty"`
	Turn            int          `json:"turn,omitempty"`
	Options         StartOptions `json:"options"`
}

type Event struct {
	Seq             uint64    `json:"seq"`
	At              time.Time `json:"at"`
	Type            string    `json:"type"`
	Text            string    `json:"text,omitempty"`
	Error           string    `json:"error,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	InputID         string    `json:"input_id,omitempty"`
	QuestionID      string    `json:"question_id,omitempty"`
	QuestionText    string    `json:"question_text,omitempty"`
	QuestionChoices []string  `json:"question_choices,omitempty"`
	QuestionKind    string    `json:"question_kind,omitempty"`
}

type Store struct{ Root string }

func (s Store) taskDir(id string) (string, error) {
	if !validID(id) {
		return "", ErrInvalidID
	}
	if strings.TrimSpace(s.Root) == "" {
		return "", errors.New("task store root is required")
	}
	return filepath.Join(s.Root, id), nil
}

func (s Store) Start(ctx context.Context, options StartOptions) (Task, error) {
	if strings.TrimSpace(options.Prompt) == "" {
		return Task{}, errors.New("prompt must not be empty")
	}
	if strings.TrimSpace(options.Workspace) == "" {
		return Task{}, errors.New("workspace must be explicit")
	}
	workspace, err := filepath.Abs(options.Workspace)
	if err != nil {
		return Task{}, fmt.Errorf("resolve workspace: %w", err)
	}
	if err = os.MkdirAll(workspace, 0o755); err != nil {
		return Task{}, fmt.Errorf("create workspace: %w", err)
	}
	st, err := os.Stat(workspace)
	if err != nil {
		return Task{}, err
	}
	if !st.IsDir() {
		return Task{}, fmt.Errorf("workspace %q is not a directory", workspace)
	}
	if options.PromptID == "" {
		options.PromptID, err = newID()
		if err != nil {
			return Task{}, err
		}
	}
	if options.Model == "" {
		options.Model = defaultModel
	}
	if options.Effort == "" {
		options.Effort = defaultEffort
	}
	options.Workspace = workspace
	if options.Executable == "" {
		options.Executable, err = os.Executable()
		if err != nil {
			return Task{}, fmt.Errorf("locate pk executable: %w", err)
		}
	}
	if options.ID == "" {
		options.ID, err = newID()
		if err != nil {
			return Task{}, err
		}
	}
	dir, err := s.taskDir(options.ID)
	if err != nil {
		return Task{}, err
	}
	root, rootErr := filepath.Abs(s.Root)
	if rootErr != nil {
		return Task{}, fmt.Errorf("resolve task store: %w", rootErr)
	}
	s.Root = root
	if err = os.MkdirAll(s.Root, 0o700); err != nil {
		return Task{}, fmt.Errorf("create task store: %w", err)
	}
	_ = os.Chmod(s.Root, 0o700)
	if err = os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Task{}, fmt.Errorf("task %q already exists", options.ID)
		}
		return Task{}, fmt.Errorf("create task directory: %w", err)
	}
	now := time.Now().UTC()
	t := Task{ID: options.ID, Status: StatusQueued, Prompt: options.Prompt, Workspace: workspace, Model: options.Model, Effort: options.Effort, ProviderID: options.ProviderID, CreatedAt: now, UpdatedAt: now, Options: options}
	if err = writeTask(dir, t); err != nil {
		os.RemoveAll(dir)
		return Task{}, err
	}
	if _, err = appendEvent(dir, &t, Event{Type: "created", Text: "Task created."}); err != nil {
		return Task{}, err
	}
	busy, unlock, lockErr := tryTaskLock(dir)
	if lockErr != nil {
		return t, lockErr
	}
	if busy {
		return t, ErrAlreadyRunning
	}
	launchBusy, launchUnlock, launchErr := tryLaunchLock(dir)
	if launchErr != nil {
		unlock()
		return t, launchErr
	}
	if launchBusy {
		unlock()
		return t, ErrAlreadyRunning
	}
	if err = s.launch(ctx, dir, &t, options.Executable); err != nil {
		unlock()
		launchUnlock()
		t.Status = StatusFailed
		t.Error = err.Error()
		t.UpdatedAt = time.Now().UTC()
		_ = writeTask(dir, t)
		return t, err
	}
	unlock()
	err = waitWorkerReady(ctx, filepath.Join(dir, "worker.ready"))
	launchUnlock()
	if err != nil {
		return t, fmt.Errorf("worker startup: %w", err)
	}
	return t, nil
}

func (s Store) launch(ctx context.Context, dir string, t *Task, executable string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, "cancel.request"))
	_ = os.Remove(filepath.Join(dir, "worker.ready"))
	cmd := exec.Command(executable, "__task-worker", t.ID)
	cmd.Dir = t.Workspace
	cmd.Env = append(os.Environ(), "PK_TASK_STORE="+s.Root)
	log, err := os.OpenFile(filepath.Join(dir, "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd.Stdout = log
	cmd.Stderr = log
	configureDetached(cmd)
	if err = cmd.Start(); err != nil {
		log.Close()
		return fmt.Errorf("start detached worker: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	_ = log.Close()
	now := time.Now().UTC()
	t.Status = StatusRunning
	t.PID = cmd.Process.Pid
	t.StartedAt = &now
	t.FinishedAt = nil
	t.Error = ""
	t.UpdatedAt = now
	if err = writeTask(dir, *t); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("save worker process: %w", err)
	}
	// The gate prevents the child from running against an incompletely published manifest.
	f, err := os.OpenFile(filepath.Join(dir, "launch.ready"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		err = f.Close()
	}
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("release worker: %w", err)
	}
	return nil
}

func (s Store) Get(id string) (Task, error) {
	dir, err := s.taskDir(id)
	if err != nil {
		return Task{}, err
	}
	t, err := readTask(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return s.reconcile(dir, t)
}

func (s Store) reconcile(dir string, t Task) (Task, error) {
	if t.Status != StatusRunning {
		return t, nil
	}
	busy, release, err := tryTaskLock(dir)
	if err != nil {
		return t, err
	}
	if busy {
		return t, nil
	}
	defer release()
	launchBusy, launchRelease, lockErr := tryLaunchLock(dir)
	if lockErr != nil {
		return t, lockErr
	}
	if launchBusy {
		launchRelease()
		return t, nil
	}
	defer launchRelease()
	// The OS lock is authoritative and is released on crash/reboot. PID is
	// informational only, avoiding PID-reuse false positives.
	latest, err := readTask(dir)
	if err != nil {
		return t, err
	}
	if latest.Status != StatusRunning {
		return latest, nil
	}
	latest.Status = StatusInterrupted
	latest.Error = "worker exited before completion"
	latest.PID = 0
	latest.UpdatedAt = time.Now().UTC()
	if err = writeTask(dir, latest); err != nil {
		return latest, err
	}
	_, _ = appendEvent(dir, &latest, Event{Type: "interrupted", Error: latest.Error})
	return latest, nil
}
func (s Store) List() ([]Task, error) {
	if strings.TrimSpace(s.Root) == "" {
		return nil, errors.New("task store root is required")
	}
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return []Task{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		t, err := s.Get(e.Name())
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Resume reruns the persisted prompt after an interrupted worker has exited.
// It refuses to start while either the worker or launch claim is held.
func (s Store) Resume(ctx context.Context, id string) (Task, error) {
	dir, err := s.taskDir(id)
	if err != nil {
		return Task{}, err
	}
	t, err := s.Get(id)
	if err != nil {
		return Task{}, err
	}
	if t.Status == StatusSucceeded {
		return t, errors.New("completed tasks cannot be resumed")
	}
	busy, unlock, err := tryTaskLock(dir)
	if err != nil {
		return t, err
	}
	if busy {
		unlock()
		return t, ErrAlreadyRunning
	}
	launchBusy, launchUnlock, err := tryLaunchLock(dir)
	if err != nil {
		unlock()
		return t, err
	}
	if launchBusy {
		launchUnlock()
		unlock()
		return t, ErrAlreadyRunning
	}
	t, err = readTask(dir)
	if err != nil {
		launchUnlock()
		unlock()
		return t, err
	}
	if t.Status == StatusSucceeded {
		launchUnlock()
		unlock()
		return t, errors.New("completed tasks cannot be resumed")
	}
	if t.Status == StatusRunning || t.Status == StatusQueued {
		t.Status = StatusInterrupted
		t.Error = "worker exited before completion"
		t.PID = 0
		t.UpdatedAt = time.Now().UTC()
		if err = writeTask(dir, t); err != nil {
			launchUnlock()
			unlock()
			return t, err
		}
		_, _ = appendEvent(dir, &t, Event{Type: "interrupted", Error: t.Error})
	}
	exe := t.Options.Executable
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			launchUnlock()
			unlock()
			return t, err
		}
	}
	if t.SessionID != "" && t.PendingPromptID == "" {
		t.PendingPromptID, err = newID()
		if err != nil {
			launchUnlock()
			unlock()
			return t, err
		}
	}
	t.Attempts++
	t.Status = StatusQueued
	t.UpdatedAt = time.Now().UTC()
	if err = writeTask(dir, t); err != nil {
		launchUnlock()
		unlock()
		return t, err
	}
	err = s.launch(ctx, dir, &t, exe)
	unlock()
	if err != nil {
		launchUnlock()
		return t, err
	}
	err = waitWorkerReady(ctx, filepath.Join(dir, "worker.ready"))
	launchUnlock()
	if err != nil {
		return t, fmt.Errorf("worker startup: %w", err)
	}
	return t, nil
}
func (s Store) Cancel(id string) error {
	dir, err := s.taskDir(id)
	if err != nil {
		return err
	}
	unlock, err := openInputLock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	t, err := readTask(dir)
	if err != nil {
		return err
	}
	if t.Status != StatusRunning {
		return errors.New("task is not running")
	}
	if err = cancelPendingQuestionsLocked(dir); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "cancel.request"), []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
}

// Follow writes output events newer than afterSeq and waits until a terminal state.
// It returns the last sequence observed; reconnect by passing that cursor again.
func (s Store) Follow(ctx context.Context, id string, afterSeq uint64, out io.Writer) (uint64, error) {
	return s.FollowEvents(ctx, id, afterSeq, out, nil)
}

// FollowEvents is Follow with a callback for each persisted event. The callback
// runs in cursor order and should not block for long; reconnect using the
// returned cursor if the method returns an error.
func (s Store) FollowEvents(ctx context.Context, id string, afterSeq uint64, out io.Writer, onEvent func(Event)) (uint64, error) {
	dir, err := s.taskDir(id)
	if err != nil {
		return afterSeq, err
	}
	var offset int64
	for {
		events, more, e := readEventsFrom(dir, afterSeq, &offset)
		if e != nil {
			return afterSeq, e
		}
		for _, event := range events {
			afterSeq = event.Seq
			if onEvent != nil {
				onEvent(event)
			}
			if event.Type == "output" && out != nil {
				if _, e = io.WriteString(out, event.Text); e != nil {
					return afterSeq, e
				}
			}
		}
		if more {
			continue
		}
		t, e := s.Get(id)
		if e != nil {
			return afterSeq, e
		}
		if terminal(t.Status) {
			return afterSeq, nil
		}
		select {
		case <-ctx.Done():
			return afterSeq, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func RunWorker(ctx context.Context, id string, run func(context.Context, WorkerOptions, io.Writer) error) error {
	if run == nil {
		return errors.New("worker runner is required")
	}
	// Resolve the store from the path encoded in PK_TASK_STORE by the parent CLI.
	root := os.Getenv("PK_TASK_STORE")
	if root == "" {
		return errors.New("PK_TASK_STORE is not set")
	}
	s := Store{Root: root}
	dir, err := s.taskDir(id)
	if err != nil {
		return err
	}
	unlock, err := lockTask(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if err = os.WriteFile(filepath.Join(dir, "worker.ready"), []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		return err
	}
	if err = waitForFile(ctx, filepath.Join(dir, "launch.ready")); err != nil {
		return err
	}
	t, err := readTask(dir)
	if err != nil {
		return err
	}
	if t.PID != os.Getpid() {
		return ErrAlreadyRunning
	}
	if t.Status != StatusRunning {
		return fmt.Errorf("task is not running (status %s)", t.Status)
	}
	_ = os.Remove(filepath.Join(dir, "launch.ready"))
	inputUnlock, e := openInputLock(dir)
	if e != nil {
		return e
	}
	inputFile, e := os.OpenFile(filepath.Join(dir, "inputs.jsonl"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if e == nil {
		e = repairInputTail(inputFile)
		_ = inputFile.Close()
	}
	inputUnlock()
	if e != nil {
		return e
	}
	ctx, stop := signalContext(ctx)
	defer stop()
	ctx, cancelRequest := watchCancel(ctx, dir)
	defer cancelRequest()
	w := &eventWriter{dir: dir, task: &t}
	_, _ = appendEvent(dir, &t, Event{Type: "started", Text: "Worker started."})
	var runErr error
	continuing := t.Attempts >= 1
	for {
		inputCtx, cancelInputs := context.WithCancel(ctx)
		turnCtx, cancelTurn := context.WithCancel(ctx)
		inputs := make(chan Input, 32)
		inputPumpDone := make(chan error, 1)
		go func() {
			err := pumpInputs(inputCtx, dir, inputs)
			inputPumpDone <- err
			if err != nil {
				cancelTurn()
			}
		}()
		runOptions := t.Options
		if continuing && t.SessionID != "" {
			runOptions.Prompt = "Continue the previous task without repeating completed work. Original request:\n\n" + t.Options.Prompt
			if t.PendingPromptID != "" {
				runOptions.PromptID = t.PendingPromptID
			}
		}
		if runOptions.PromptID == "" {
			runOptions.PromptID = t.Options.PromptID
		}
		options := WorkerOptions{StartOptions: runOptions, Inputs: inputs, SessionID: t.SessionID, Resume: continuing, KeepAlive: false, OnSession: func(sessionID string) {
			w.mu.Lock()
			defer w.mu.Unlock()
			_, _ = appendEvent(dir, &t, Event{Type: "session", SessionID: sessionID})
		}}
		runErr = run(turnCtx, options, w)
		cancelTurn()
		cancelInputs()
		inputPumpErr := <-inputPumpDone
		if inputPumpErr != nil {
			runErr = inputPumpErr
		}
		if ctx.Err() != nil && runErr == nil {
			runErr = ctx.Err()
		}
		if runErr != nil {
			break
		}
		// Serialize the terminal transition with SendInput. Anything enqueued
		// before this lock is handed back to runner in a continuation turn.
		inputUnlock, e := openInputLock(dir)
		if e != nil {
			runErr = e
			break
		}
		if _, cancelErr := os.Stat(filepath.Join(dir, "cancel.request")); cancelErr == nil {
			_ = os.Remove(filepath.Join(dir, "cancel.request"))
			e = finishTask(dir, StatusCanceled, "canceled")
			inputUnlock()
			return e
		}
		pending, e := hasPendingInputs(dir)
		if e != nil {
			inputUnlock()
			runErr = e
			break
		}
		if pending {
			t, e = readTask(dir)
			if e == nil {
				t.Turn++
				t.PendingPromptID, e = newID()
				t.UpdatedAt = time.Now().UTC()
				if e == nil {
					e = writeTask(dir, t)
				}
			}
			inputUnlock()
			if e != nil {
				runErr = e
				break
			}
			continuing = true
			continue
		}
		e = finishTask(dir, StatusSucceeded, "")
		inputUnlock()
		return e
	}
	status, message := StatusFailed, runErr.Error()
	if errors.Is(runErr, context.Canceled) {
		status, message = StatusCanceled, "canceled"
	}
	eventErr := finishTask(dir, status, message)
	if runErr != nil {
		return runErr
	}
	return eventErr
}

// SendInput appends a follow-up prompt for an attached task. The active worker
// forwards queued inputs to runner.Run in order.
func (s Store) SendInput(id, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("input must not be empty")
	}
	if len(text) > 64<<10 {
		return errors.New("input exceeds 64 KiB")
	}
	dir, err := s.taskDir(id)
	if err != nil {
		return err
	}
	unlock, err := openInputLock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	t, err := readTask(dir)
	if err != nil {
		return err
	}
	if t.Status != StatusRunning {
		return errors.New("task is not running")
	}
	inputID, err := newID()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "inputs.jsonl"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err = repairInputTail(f); err != nil {
		f.Close()
		return err
	}
	envelope := inputEnvelope{ID: inputID, Text: text}
	b, err := json.Marshal(envelope)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if ce := f.Close(); err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	// The queue is authoritative; an event log failure must not invite a retry
	// with a new ID after the durable prompt has already been appended.
	_, _ = appendEvent(dir, &t, Event{Type: "input", InputID: inputID})
	return nil
}

func hasPendingInputs(dir string) (bool, error) {
	var offset int64
	if b, e := os.ReadFile(filepath.Join(dir, "input.offset")); e == nil {
		_, _ = fmt.Sscanf(string(b), "%d", &offset)
	} else if !errors.Is(e, os.ErrNotExist) {
		return false, e
	}
	info, e := os.Stat(filepath.Join(dir, "inputs.jsonl"))
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return info.Size() > offset, nil
}

func repairInputTail(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	tailSize := size
	if tailSize > 128*1024 {
		tailSize = 128 * 1024
	}
	buf := make([]byte, tailSize)
	if _, err = f.ReadAt(buf, size-tailSize); err != nil {
		return err
	}
	if buf[len(buf)-1] == '\n' {
		return nil
	}
	last := bytes.LastIndexByte(buf, '\n')
	start := last + 1
	var env inputEnvelope
	if json.Unmarshal(buf[start:], &env) == nil && env.ID != "" {
		if _, err = f.WriteAt([]byte{'\n'}, size); err != nil {
			return err
		}
		return f.Sync()
	}
	truncateTo := size - tailSize + int64(maxInt(last+1, 0))
	return f.Truncate(truncateTo)
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type inputEnvelope struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func pumpInputs(ctx context.Context, dir string, out chan<- Input) error {
	defer close(out)
	offsetPath := filepath.Join(dir, "input.offset")
	var offset int64
	if b, e := os.ReadFile(offsetPath); e == nil {
		if _, e = fmt.Sscanf(string(b), "%d", &offset); e != nil || offset < 0 {
			return fmt.Errorf("corrupt task input offset %q", string(b))
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return fmt.Errorf("read task input offset: %w", e)
	}
	for {
		f, err := os.Open(filepath.Join(dir, "inputs.jsonl"))
		if err == nil {
			_, _ = f.Seek(offset, io.SeekStart)
			reader := bufio.NewReader(f)
			for {
				line, e := reader.ReadString('\n')
				if e != nil {
					if !errors.Is(e, io.EOF) {
						_ = f.Close()
						return fmt.Errorf("read task input queue: %w", e)
					}
					break
				}
				next := offset + int64(len(line))
				var env inputEnvelope
				if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &env); err != nil || env.ID == "" || strings.TrimSpace(env.Text) == "" {
					_ = f.Close()
					return fmt.Errorf("corrupt task input queue record at byte %d", offset)
				}
				ack := make(chan error, 1)
				input := Input{ID: env.ID, Text: env.Text, Ack: func(ackErr error) {
					// Persist the cursor synchronously with the runner's durable ack,
					// before callback return can race worker cancellation.
					if ackErr == nil {
						if e := writeOffset(offsetPath, next); e != nil {
							ackErr = e
						}
					}
					if ackErr == nil {
						_, _ = appendEvent(dir, &Task{}, Event{Type: "input_accepted", InputID: env.ID})
					} else {
						_, _ = appendEvent(dir, &Task{}, Event{Type: "input_rejected", InputID: env.ID, Error: ackErr.Error()})
					}
					select {
					case ack <- ackErr:
					default:
					}
				}}
				select {
				case out <- input:
				case <-ctx.Done():
					f.Close()
					return nil
				}
				select {
				case ackErr := <-ack:
					if ackErr == nil {
						offset = next
					} else {
						_ = f.Close()
						return fmt.Errorf("persist accepted task input %q: %w", env.ID, ackErr)
					}
				case <-ctx.Done():
					_ = f.Close()
					return nil
				}
				if ctx.Err() != nil {
					_ = f.Close()
					return nil
				}
			}
			f.Close()
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("open task input queue: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func writeOffset(path string, offset int64) error {
	f, err := os.CreateTemp(filepath.Dir(path), "input-offset-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err = f.Chmod(0o600); err == nil {
		_, err = fmt.Fprintf(f, "%d", offset)
	}
	if err == nil {
		err = f.Sync()
	}
	if ce := f.Close(); err == nil {
		err = ce
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

type eventWriter struct {
	mu   sync.Mutex
	dir  string
	task *Task
}

func (w *eventWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := len(p)
	for len(p) > 0 {
		n := len(p)
		if n > 8192 {
			n = 8192
		}
		if _, err := appendEvent(w.dir, w.task, Event{Type: "output", Text: string(p[:n])}); err != nil {
			return 0, err
		}
		p = p[n:]
	}
	return total, nil
}

func appendEvent(dir string, t *Task, e Event) (Event, error) {
	unlock, err := openEventLock(dir)
	if err != nil {
		return Event{}, err
	}
	defer unlock()
	return appendEventLocked(dir, t, e)
}

func appendEventLocked(dir string, t *Task, e Event) (Event, error) {
	latest, err := readTask(dir)
	if err != nil {
		return Event{}, err
	}
	f, tailSeq, validSize, err := repairEventTail(dir)
	if err != nil {
		return Event{}, err
	}
	defer f.Close()
	if err = f.Truncate(validSize); err != nil {
		return Event{}, err
	}
	if _, err = f.Seek(0, io.SeekEnd); err != nil {
		return Event{}, err
	}
	seq := latest.LastEvent
	if tailSeq > seq {
		seq = tailSeq
	}
	e.Seq = seq + 1
	e.At = time.Now().UTC()
	if len(e.Text) > 8<<10 {
		e.Text = e.Text[:8<<10]
	}
	if len(e.Error) > 8<<10 {
		e.Error = e.Error[:8<<10]
	}
	b, err := json.Marshal(e)
	if err != nil {
		return Event{}, err
	}
	if _, err = f.Write(append(b, '\n')); err != nil {
		return Event{}, err
	}
	if err = f.Sync(); err != nil {
		return Event{}, err
	}
	if e.Type == "session" {
		latest.SessionID = e.SessionID
	}
	latest.LastEvent = e.Seq
	latest.UpdatedAt = e.At
	*t = latest
	if err = writeTask(dir, latest); err != nil {
		return Event{}, err
	}
	return e, nil
}

// finishTask serializes the terminal manifest update with event appends. This
// keeps LastEvent monotonic when a late output/input event races worker exit.
func finishTask(dir string, status Status, message string) error {
	unlock, err := openEventLock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	latest, err := readTask(dir)
	if err != nil {
		return err
	}
	event, err := appendEventLocked(dir, &latest, Event{Type: string(status), Error: message})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	latest.Status = status
	latest.Error = message
	latest.PID = 0
	latest.FinishedAt = &now
	latest.UpdatedAt = now
	latest.PendingPromptID = ""
	latest.LastEvent = event.Seq
	return writeTask(dir, latest)
}

// repairEventTail truncates only a partial trailing line and returns the last
// complete sequence, making events.jsonl the authority after a crash.
func repairEventTail(dir string) (*os.File, uint64, int64, error) {
	path := filepath.Join(dir, "events.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, 0, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, 0, err
	}
	size := info.Size()
	if size == 0 {
		return f, 0, 0, nil
	}
	const tailLimit = 128 * 1024
	start := size - tailLimit
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err = f.ReadAt(buf, start); err != nil {
		f.Close()
		return nil, 0, 0, err
	}
	lastNL := bytes.LastIndexByte(buf, '\n')
	if lastNL < 0 {
		if err = f.Truncate(0); err != nil {
			f.Close()
			return nil, 0, 0, err
		}
		return f, 0, 0, nil
	}
	validSize := start + int64(lastNL+1)
	if validSize != size {
		if err = f.Truncate(validSize); err != nil {
			f.Close()
			return nil, 0, 0, err
		}
		buf = buf[:lastNL+1]
	}
	prior := bytes.LastIndexByte(buf[:len(buf)-1], '\n')
	line := buf[prior+1 : len(buf)-1]
	var event Event
	if err = json.Unmarshal(line, &event); err != nil {
		f.Close()
		return nil, 0, 0, fmt.Errorf("read last task event: %w", err)
	}
	return f, event.Seq, validSize, nil
}

func readEventsFrom(dir string, after uint64, offset *int64) ([]Event, bool, error) {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if _, err = f.Seek(*offset, io.SeekStart); err != nil {
		return nil, false, err
	}
	var out []Event
	reader := bufio.NewReader(f)
	for {
		if len(out) >= 256 {
			return out, true, nil
		}
		line, e := reader.ReadString('\n')
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, false, e
		}
		*offset += int64(len(line))
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			continue
		}
		var event Event
		if err = json.Unmarshal([]byte(line), &event); err != nil {
			return nil, false, err
		}
		if event.Seq > after {
			out = append(out, event)
		}
	}
	return out, false, nil
}
func writeTask(dir string, t Task) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "manifest-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if ce := f.Close(); err == nil {
		err = ce
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, filepath.Join(dir, "manifest.json")); err != nil {
		return err
	}
	return syncDirectory(dir)
}
func readTask(dir string) (Task, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Task{}, err
	}
	var t Task
	if err = json.Unmarshal(b, &t); err != nil {
		return Task{}, err
	}
	return t, nil
}
func waitWorkerReady(ctx context.Context, path string) error {
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return waitForFile(readyCtx, path)
}

func waitForFile(ctx context.Context, path string) error {
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func terminal(s Status) bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCanceled || s == StatusInterrupted
}
func validID(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func newID() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "task-" + hex.EncodeToString(b), nil
}
