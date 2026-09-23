// Package interaction connects foreground model questions to the RPC/TUI host.
// It intentionally has no filesystem or terminal dependencies.
package interaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	ErrQuestionNotFound = errors.New("question is not pending")
	ErrQuestionAnswered = errors.New("question was already answered")
)

type Question struct {
	ID        string   `json:"id"`
	Text      string   `json:"text"`
	Choices   []string `json:"choices,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
	Kind      string   `json:"kind,omitempty"`
}

type answer struct {
	text string
	err  error
}
type pendingQuestion struct {
	question Question
	answer   chan answer
	answered bool
}

// Broker owns pending questions for a single foreground runner turn. Use its
// Context for the runner so Cancel also stops model continuation.
type Broker struct {
	ctx       context.Context
	cancel    context.CancelFunc
	sessionID string
	questions chan Question
	mu        sync.Mutex
	pending   map[string]*pendingQuestion
}

func NewBroker(parent context.Context, sessionID string) *Broker {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &Broker{ctx: ctx, cancel: cancel, sessionID: sessionID, questions: make(chan Question, 16), pending: make(map[string]*pendingQuestion)}
}

func (b *Broker) Context() context.Context {
	if b == nil {
		return context.Background()
	}
	return b.ctx
}
func (b *Broker) Questions() <-chan Question {
	if b == nil {
		return nil
	}
	return b.questions
}

// Answer completes one request with an actual user answer. Empty answers are
// rejected so a timeout, dismissed overlay, or unanswered question cannot be
// mistaken for consent or useful input.
func (b *Broker) Answer(id, text string) error {
	if b == nil {
		return errors.New("question broker is unavailable")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("answer must not be empty")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	request, ok := b.pending[id]
	if !ok {
		return ErrQuestionNotFound
	}
	if request.answered {
		return ErrQuestionAnswered
	}
	request.answered = true
	select {
	case request.answer <- answer{text: text}:
		return nil
	case <-b.ctx.Done():
		return b.ctx.Err()
	}
}

// Cancel aborts the active foreground turn. The model receives no synthetic
// answer and cannot continue from an unanswered question.
func (b *Broker) Cancel(id string) error {
	if b == nil {
		return errors.New("question broker is unavailable")
	}
	b.mu.Lock()
	_, ok := b.pending[id]
	b.mu.Unlock()
	if !ok {
		return ErrQuestionNotFound
	}
	b.cancel()
	return nil
}

// Close releases a blocked AskUser call when the owning run ends or is
// interrupted. It is safe to call more than once.
func (b *Broker) Close() {
	if b != nil {
		b.cancel()
	}
}

func (b *Broker) ask(question Question) (string, error) {
	if b == nil {
		return "", errors.New("AskUser is only available in foreground interactive sessions")
	}
	if strings.TrimSpace(question.ID) == "" {
		return "", errors.New("AskUser call id must not be empty")
	}
	if strings.TrimSpace(question.Text) == "" {
		return "", errors.New("AskUser question must not be empty")
	}
	request := &pendingQuestion{question: question, answer: make(chan answer, 1)}
	b.mu.Lock()
	if _, exists := b.pending[question.ID]; exists {
		b.mu.Unlock()
		return "", fmt.Errorf("AskUser request %q is already pending", question.ID)
	}
	b.pending[question.ID] = request
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.pending[question.ID] == request {
			delete(b.pending, question.ID)
		}
		b.mu.Unlock()
	}()
	select {
	case b.questions <- question:
	case <-b.ctx.Done():
		return "", b.ctx.Err()
	}
	select {
	case result := <-request.answer:
		return result.text, result.err
	case <-b.ctx.Done():
		return "", b.ctx.Err()
	}
}
