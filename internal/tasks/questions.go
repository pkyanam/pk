package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrQuestionNotFound = errors.New("task question not found")
	ErrQuestionCanceled = errors.New("task question was canceled")
)

const (
	questionPending  = "pending"
	questionAnswered = "answered"
	questionCanceled = "canceled"
	maxTaskQuestions = 32
)

// Question is a durable AskUser request owned by one task. Its ID is the
// interaction tool call ID and remains stable when a worker resumes.
type Question struct {
	ID        string   `json:"id"`
	SessionID string   `json:"session_id,omitempty"`
	Text      string   `json:"text"`
	Choices   []string `json:"choices,omitempty"`
	Kind      string   `json:"kind,omitempty"`
}

type questionRecord struct {
	Question
	Status    string    `json:"status"`
	Answer    string    `json:"answer,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type questionFile struct {
	Version int              `json:"version"`
	Records []questionRecord `json:"records"`
}

// AskQuestion publishes a question once, then waits for an explicit durable
// answer. If the worker is restarted with the same question ID, an existing
// answer is returned or the existing pending request is reused.
func (s Store) AskQuestion(ctx context.Context, taskID string, question Question) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateQuestion(question); err != nil {
		return "", err
	}
	dir, err := s.taskDir(taskID)
	if err != nil {
		return "", err
	}
	unlock, err := openInputLock(dir)
	if err != nil {
		return "", err
	}
	state, err := readQuestionFile(dir)
	if err != nil {
		unlock()
		return "", err
	}
	record, found := findQuestion(state, question.ID)
	if found {
		if !sameQuestion(record.Question, question) {
			unlock()
			return "", fmt.Errorf("task question ID %q was reused with different content", question.ID)
		}
		if record.Status == questionAnswered {
			answer := record.Answer
			if err = ensureQuestionEvent(dir, Event{Type: "task_question", SessionID: question.SessionID, QuestionID: question.ID, QuestionText: question.Text, QuestionChoices: append([]string(nil), question.Choices...), QuestionKind: question.Kind}); err == nil {
				err = ensureQuestionEvent(dir, Event{Type: "task_question_answered", QuestionID: question.ID})
			}
			unlock()
			if err != nil {
				return "", err
			}
			return answer, nil
		}
		if record.Status == questionCanceled {
			unlock()
			return "", ErrQuestionCanceled
		}
	} else {
		if len(state.Records) >= maxTaskQuestions {
			unlock()
			return "", errors.New("task has reached its durable question limit")
		}
		task, readErr := readTask(dir)
		if readErr != nil {
			unlock()
			return "", readErr
		}
		if task.Status != StatusRunning {
			unlock()
			return "", fmt.Errorf("cannot ask a question while task is %s", task.Status)
		}
		now := time.Now().UTC()
		record = questionRecord{Question: cloneQuestion(question), Status: questionPending, CreatedAt: now, UpdatedAt: now}
		state.Records = append(state.Records, record)
		if err = writeQuestionFile(dir, state); err != nil {
			unlock()
			return "", err
		}
	}
	// The file is the durable source of truth. Repair an event omitted by a
	// crash between publishing state and appending its notification.
	if err = ensureQuestionEvent(dir, Event{Type: "task_question", SessionID: question.SessionID, QuestionID: question.ID, QuestionText: question.Text, QuestionChoices: append([]string(nil), question.Choices...), QuestionKind: question.Kind}); err != nil {
		unlock()
		return "", err
	}
	unlock()

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		unlock, err = openInputLock(dir)
		if err != nil {
			return "", err
		}
		state, err = readQuestionFile(dir)
		if err == nil {
			record, found = findQuestion(state, question.ID)
			if !found {
				err = ErrQuestionNotFound
			} else if record.Status == questionAnswered {
				answer := record.Answer
				unlock()
				return answer, nil
			} else if record.Status == questionCanceled {
				unlock()
				return "", ErrQuestionCanceled
			} else {
				task, taskErr := readTask(dir)
				if taskErr != nil {
					err = taskErr
				} else if task.Status != StatusRunning {
					err = fmt.Errorf("task is %s; pending question is no longer active", task.Status)
				}
			}
		}
		unlock()
		if err != nil {
			return "", err
		}
		timer := time.NewTimer(300 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

// ListQuestions returns only unresolved questions, suitable for restoring an
// attached-task overlay. Answer text is never returned by this method.
func (s Store) ListQuestions(taskID string) ([]Question, error) {
	dir, err := s.taskDir(taskID)
	if err != nil {
		return nil, err
	}
	unlock, err := openInputLock(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	state, err := readQuestionFile(dir)
	if err != nil {
		return nil, err
	}
	task, err := readTask(dir)
	if err != nil {
		return nil, err
	}
	if task.Status != StatusRunning {
		return []Question{}, nil
	}
	questions := make([]Question, 0)
	for _, record := range state.Records {
		if record.Status == questionPending {
			questions = append(questions, cloneQuestion(record.Question))
		}
	}
	return questions, nil
}

// AnswerQuestion records an explicit user answer durably before returning.
// Repeating the same answer is idempotent, which lets a client retry after a
// lost acknowledgement without creating another answer.
func (s Store) AnswerQuestion(taskID, questionID, answer string) error {
	if strings.TrimSpace(answer) == "" {
		return errors.New("answer must not be empty")
	}
	if len(answer) > 64<<10 {
		return errors.New("answer exceeds 64 KiB")
	}
	dir, err := s.taskDir(taskID)
	if err != nil {
		return err
	}
	unlock, err := openInputLock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := readQuestionFile(dir)
	if err != nil {
		return err
	}
	record, found := findQuestion(state, questionID)
	if !found {
		return ErrQuestionNotFound
	}
	if record.Status == questionCanceled {
		return ErrQuestionCanceled
	}
	if record.Status == questionAnswered {
		if record.Answer != answer {
			return errors.New("task question already has a different answer")
		}
	} else {
		task, readErr := readTask(dir)
		if readErr != nil {
			return readErr
		}
		if task.Status != StatusRunning {
			return fmt.Errorf("task is %s; question is no longer answerable", task.Status)
		}
		record.Status = questionAnswered
		record.Answer = answer
		record.UpdatedAt = time.Now().UTC()
		storeQuestion(state, record)
		if err = writeQuestionFile(dir, state); err != nil {
			return err
		}
	}
	if err = ensureQuestionEvent(dir, Event{Type: "task_question", SessionID: record.SessionID, QuestionID: record.ID, QuestionText: record.Text, QuestionChoices: append([]string(nil), record.Choices...), QuestionKind: record.Kind}); err != nil {
		return err
	}
	return ensureQuestionEvent(dir, Event{Type: "task_question_answered", QuestionID: questionID})
}

// CancelQuestion cancels the owning task rather than producing an answer.
func (s Store) CancelQuestion(taskID, questionID string) error {
	dir, err := s.taskDir(taskID)
	if err != nil {
		return err
	}
	unlock, err := openInputLock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := readQuestionFile(dir)
	if err != nil {
		return err
	}
	record, found := findQuestion(state, questionID)
	if !found {
		return ErrQuestionNotFound
	}
	if record.Status == questionAnswered {
		return errors.New("task question has already been answered")
	}
	if record.Status != questionCanceled {
		record.Status = questionCanceled
		record.UpdatedAt = time.Now().UTC()
		storeQuestion(state, record)
		if err = writeQuestionFile(dir, state); err != nil {
			return err
		}
	}
	if err = ensureQuestionEvent(dir, Event{Type: "task_question_cancelled", QuestionID: questionID}); err != nil {
		return err
	}
	if err = cancelPendingQuestionsLocked(dir); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "cancel.request"), []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
}

// cancelPendingQuestionsLocked marks every unresolved question canceled. The
// caller must hold inputs.lock so an answer cannot race task cancellation.
func cancelPendingQuestionsLocked(dir string) error {
	state, err := readQuestionFile(dir)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	changed := false
	for i := range state.Records {
		if state.Records[i].Status != questionPending {
			continue
		}
		state.Records[i].Status = questionCanceled
		state.Records[i].UpdatedAt = now
		changed = true
	}
	if changed {
		if err = writeQuestionFile(dir, state); err != nil {
			return err
		}
	}
	for _, record := range state.Records {
		if record.Status == questionCanceled {
			if err = ensureQuestionEvent(dir, Event{Type: "task_question_cancelled", QuestionID: record.ID}); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateQuestion(question Question) error {
	if strings.TrimSpace(question.ID) == "" || len(question.ID) > 256 {
		return errors.New("question ID is required and must be at most 256 bytes")
	}
	if strings.TrimSpace(question.Text) == "" || len(question.Text) > 16<<10 {
		return errors.New("question text is required and must be at most 16 KiB")
	}
	if len(question.Choices) > 32 {
		return errors.New("question has more than 32 choices")
	}
	for _, choice := range question.Choices {
		if strings.TrimSpace(choice) == "" || len(choice) > 1024 {
			return errors.New("question choices must be nonempty and at most 1 KiB")
		}
	}
	if len(question.Kind) > 64 || len(question.SessionID) > 256 {
		return errors.New("question metadata exceeds its size limit")
	}
	event, err := json.Marshal(Event{Type: "task_question", SessionID: question.SessionID, QuestionID: question.ID, QuestionText: question.Text, QuestionChoices: question.Choices, QuestionKind: question.Kind})
	if err != nil {
		return err
	}
	if len(event) > 64<<10 {
		return errors.New("encoded task question exceeds 64 KiB")
	}
	return nil
}

func sameQuestion(a, b Question) bool {
	if a.ID != b.ID || a.SessionID != b.SessionID || a.Text != b.Text || a.Kind != b.Kind || len(a.Choices) != len(b.Choices) {
		return false
	}
	for i := range a.Choices {
		if a.Choices[i] != b.Choices[i] {
			return false
		}
	}
	return true
}

func cloneQuestion(q Question) Question {
	q.Choices = append([]string(nil), q.Choices...)
	return q
}

func questionPath(dir string) string { return filepath.Join(dir, "questions.json") }

func readQuestionFile(dir string) (questionFile, error) {
	f, err := os.Open(questionPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return questionFile{Version: 1}, nil
	}
	if err != nil {
		return questionFile{}, err
	}
	b, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	closeErr := f.Close()
	if err != nil {
		return questionFile{}, err
	}
	if closeErr != nil {
		return questionFile{}, closeErr
	}
	if len(b) > 4<<20 {
		return questionFile{}, errors.New("task question state exceeds 4 MiB")
	}
	var state questionFile
	if err = json.Unmarshal(b, &state); err != nil {
		return questionFile{}, fmt.Errorf("read task question state: %w", err)
	}
	if state.Version != 1 || len(state.Records) > maxTaskQuestions {
		return questionFile{}, errors.New("invalid task question state")
	}
	return state, nil
}

func writeQuestionFile(dir string, state questionFile) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(b) > 4<<20 {
		return errors.New("task question state exceeds 4 MiB")
	}
	tmp, err := os.CreateTemp(dir, "questions-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, questionPath(dir))
	}
	if err != nil {
		_ = os.Remove(name)
		return err
	}
	return syncDirectory(dir)
}

func findQuestion(state questionFile, id string) (questionRecord, bool) {
	for _, record := range state.Records {
		if record.ID == id {
			return record, true
		}
	}
	return questionRecord{}, false
}

func storeQuestion(state questionFile, updated questionRecord) {
	for i := range state.Records {
		if state.Records[i].ID == updated.ID {
			state.Records[i] = updated
			return
		}
	}
}

func ensureQuestionEvent(dir string, want Event) error {
	var cursor uint64
	var offset int64
	for {
		events, more, err := readEventsFrom(dir, cursor, &offset)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Type == want.Type && event.QuestionID == want.QuestionID {
				return nil
			}
			cursor = event.Seq
		}
		if !more {
			break
		}
	}
	_, err := appendEvent(dir, &Task{}, want)
	return err
}
