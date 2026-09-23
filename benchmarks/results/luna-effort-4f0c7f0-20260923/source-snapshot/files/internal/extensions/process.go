package extensions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProcessFactory starts each explicitly configured extension as a child
// process. It does not reduce the child's operating-system privileges.
func ProcessFactory(parent context.Context, manifest Manifest, workspace string) (Worker, error) {
	ctx, cancel := context.WithCancel(parent)
	cmd := exec.CommandContext(ctx, manifest.Executable, manifest.Args...)
	cmd.Dir = workspace
	configureProcessGroup(cmd)
	cmd.WaitDelay = 750 * time.Millisecond
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr := &boundedBuffer{limit: 16 << 10}
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start extension %q: %w", manifest.ID, err)
	}
	worker := &processWorker{
		cmd: cmd, cancel: cancel, stdin: stdin, stderr: stderr,
		readEvents: make(chan responseEvent, 16), done: make(chan struct{}),
		callGate: make(chan struct{}, 1),
	}
	go worker.readResponses(stdout)
	go func() { _ = cmd.Wait(); close(worker.done) }()
	return worker, nil
}

type processWorker struct {
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	stdin      io.WriteCloser
	stderr     *boundedBuffer
	readEvents chan responseEvent
	done       chan struct{}
	callGate   chan struct{}
	closeOnce  sync.Once
	nextID     atomic.Uint64
}

type responseEvent struct {
	response *Response
	err      error
}

func (w *processWorker) readResponses(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxMessageSize)
	for scanner.Scan() {
		var response Response
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			w.readEvents <- responseEvent{err: fmt.Errorf("decode extension response: %w", err)}
			return
		}
		w.readEvents <- responseEvent{response: &response}
	}
	if err := scanner.Err(); err != nil {
		w.readEvents <- responseEvent{err: fmt.Errorf("read extension response: %w", err)}
		return
	}
	w.readEvents <- responseEvent{err: io.EOF}
}

func (w *processWorker) Call(ctx context.Context, method string, params any, result any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case w.callGate <- struct{}{}:
		defer func() { <-w.callGate }()
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return w.processError("worker exited")
	}
	select {
	case <-w.done:
		return w.processError("worker exited")
	default:
	}
	id := fmt.Sprintf("%d", w.nextID.Add(1))
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode extension params: %w", err)
	}
	line, err := json.Marshal(Request{ID: id, Method: method, Params: encodedParams})
	if err != nil {
		return err
	}
	if len(line) > maxMessageSize {
		return errors.New("extension request exceeds protocol message limit")
	}
	line = append(line, '\n')
	writeDone := make(chan error, 1)
	go func() { _, writeErr := w.stdin.Write(line); writeDone <- writeErr }()
	select {
	case err = <-writeDone:
		if err != nil {
			w.Close()
			return w.processError("write request", err)
		}
	case <-ctx.Done():
		w.Close()
		return ctx.Err()
	case <-w.done:
		w.Close()
		return w.processError("worker exited before reading request")
	}
	for {
		select {
		case event := <-w.readEvents:
			if event.err != nil {
				w.Close()
				return w.processError("read response", event.err)
			}
			if err = validateResponse(*event.response, id, result); err != nil {
				var remote *RPCError
				if errors.As(err, &remote) {
					return remote
				}
				w.Close()
				return err
			}
			return nil
		case <-w.done:
			// stdout EOF is ordered after all complete lines by readResponses;
			// done only protects against an abnormal pipe-reader failure.
			select {
			case event := <-w.readEvents:
				if event.response != nil {
					if err = validateResponse(*event.response, id, result); err == nil {
						return nil
					}
					return err
				}
				return w.processError("worker exited before responding", event.err)
			case <-time.After(500 * time.Millisecond):
				return w.processError("worker exited before responding")
			}
		case <-ctx.Done():
			w.Close()
			return ctx.Err()
		}
	}
}

func (w *processWorker) processError(action string, causes ...error) error {
	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, "extension %q %s", w.cmd.Path, action)
	for _, cause := range causes {
		if cause != nil {
			fmt.Fprintf(&buffer, ": %v", cause)
			break
		}
	}
	if detail := strings.TrimSpace(w.stderr.String()); detail != "" {
		if len(detail) > 1024 {
			detail = detail[:1024] + "…"
		}
		fmt.Fprintf(&buffer, " (stderr: %s)", detail)
	}
	return errors.New(buffer.String())
}

func (w *processWorker) Close() error {
	w.closeOnce.Do(func() { _ = w.stdin.Close(); w.cancel() })
	select {
	case <-w.done:
		return nil
	case <-time.After(1500 * time.Millisecond):
		w.cancel()
		select {
		case <-w.done:
			return nil
		case <-time.After(500 * time.Millisecond):
			return errors.New("extension process group did not exit after cancellation")
		}
	}
}

type boundedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = b.buffer.Write(p[:remaining])
	}
	return n, nil
}

func (b *boundedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }
