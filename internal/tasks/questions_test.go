package tasks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func questionTestStore(t *testing.T) Store {
	t.Helper()
	root := t.TempDir()
	store := Store{Root: root}
	dir := filepath.Join(root, "question-task")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := Task{ID: "question-task", Status: StatusRunning, Prompt: "test", Workspace: t.TempDir(), CreatedAt: now, UpdatedAt: now, Options: StartOptions{Prompt: "test", Workspace: t.TempDir()}}
	if err := writeTask(dir, task); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestQuestionRetriesRepairMissingEvents(t *testing.T) {
	store := questionTestStore(t)
	q := Question{ID: "call-repair", SessionID: "session-r", Text: "Choose?", Choices: []string{"yes", "no"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := store.AskQuestion(ctx, "question-task", q); done <- err }()
	waitForQuestion(t, store, q.ID)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("initial wait returned %v", err)
	}
	dir := filepath.Join(store.Root, "question-task")
	removeQuestionEvents(t, dir, q.ID, "task_question")
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	done2 := make(chan error, 1)
	go func() { _, err := store.AskQuestion(ctx2, "question-task", q); done2 <- err }()
	waitForQuestionEvent(t, dir, q.ID, "task_question")
	events, _, err := readEventsFrom(dir, 0, new(int64))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type == "task_question" && event.QuestionID == q.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("retry did not repair missing question event: %+v", events)
	}
	if err = store.AnswerQuestion("question-task", q.ID, "yes"); err != nil {
		t.Fatal(err)
	}
	if err = <-done2; err != nil {
		t.Fatal(err)
	}
	removeQuestionEvents(t, dir, q.ID, "task_question_answered")
	if err = store.AnswerQuestion("question-task", q.ID, "yes"); err != nil {
		t.Fatalf("idempotent answer did not repair ack event: %v", err)
	}
	events, _, err = readEventsFrom(dir, 0, new(int64))
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, event := range events {
		if event.Type == "task_question_answered" && event.QuestionID == q.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("same-answer retry did not repair missing acknowledgement event")
	}
}

func waitForQuestionEvent(t *testing.T, dir, id, eventType string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		events, _, err := readEventsFrom(dir, 0, new(int64))
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Type == eventType && event.QuestionID == id {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("event %s for question %s not persisted", eventType, id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForQuestion(t *testing.T, store Store, id string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		pending, err := store.ListQuestions("question-task")
		if err != nil {
			t.Fatal(err)
		}
		for _, question := range pending {
			if question.ID == id {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("question never became pending")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func removeQuestionEvents(t *testing.T, dir, id, eventType string) {
	t.Helper()
	path := filepath.Join(dir, "events.jsonl")
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		var event Event
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
			in.Close()
			t.Fatal(err)
		}
		if event.QuestionID == id && event.Type == eventType {
			continue
		}
		lines = append(lines, scanner.Text())
	}
	if err = scanner.Err(); err != nil {
		in.Close()
		t.Fatal(err)
	}
	if err = in.Close(); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n")
	if len(lines) > 0 {
		body += "\n"
	}
	if err = os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAskQuestionPersistsBeforeWaitAndAnswerIsDurable(t *testing.T) {
	store := questionTestStore(t)
	question := Question{ID: "call-1", SessionID: "session-1", Text: "Which option?", Choices: []string{"a", "b"}, Kind: "clarification"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	answerCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		answer, err := store.AskQuestion(ctx, "question-task", question)
		answerCh <- answer
		errCh <- err
	}()

	deadline := time.Now().Add(time.Second)
	for {
		pending, err := store.ListQuestions("question-task")
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 1 {
			if pending[0].ID != question.ID || pending[0].SessionID != question.SessionID || len(pending[0].Choices) != 2 {
				t.Fatalf("unexpected pending question: %+v", pending[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("question was not persisted before AskQuestion waited")
		}
		time.Sleep(5 * time.Millisecond)
	}
	events, _, err := readEventsFrom(filepath.Join(store.Root, "question-task"), 0, new(int64))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "task_question" || events[0].QuestionID != question.ID {
		t.Fatalf("question event missing: %+v", events)
	}
	if err = store.AnswerQuestion("question-task", question.ID, "b"); err != nil {
		t.Fatal(err)
	}
	if err = store.AnswerQuestion("question-task", question.ID, "b"); err != nil {
		t.Fatalf("same answer retry should be idempotent: %v", err)
	}
	select {
	case answer := <-answerCh:
		if answer != "b" {
			t.Fatalf("AskQuestion answer=%q, want b", answer)
		}
	case <-ctx.Done():
		t.Fatal("AskQuestion did not wake after durable answer")
	}
	if err = <-errCh; err != nil {
		t.Fatal(err)
	}
	if pending, err := store.ListQuestions("question-task"); err != nil || len(pending) != 0 {
		t.Fatalf("answered question remains pending: %+v, %v", pending, err)
	}
	data, err := os.ReadFile(filepath.Join(store.Root, "question-task", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"answer":"b"`) {
		t.Fatal("answer text leaked into task event log")
	}
}

func TestAskQuestionResumeReusesIDAndRejectsContentDrift(t *testing.T) {
	store := questionTestStore(t)
	question := Question{ID: "call-resume", SessionID: "session-x", Text: "Continue?"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := store.AskQuestion(ctx, "question-task", question)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		pending, _ := store.ListQuestions("question-task")
		if len(pending) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("question never became pending")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted question returned %v", err)
	}
	if _, err := store.AskQuestion(context.Background(), "question-task", Question{ID: question.ID, SessionID: question.SessionID, Text: "Different question"}); err == nil {
		t.Fatal("reused question ID with different content was accepted")
	}
	answerDone := make(chan error, 1)
	go func() {
		answer, err := store.AskQuestion(context.Background(), "question-task", question)
		if err == nil && answer != "yes" {
			err = errors.New("resumed question received wrong answer")
		}
		answerDone <- err
	}()
	if err := store.AnswerQuestion("question-task", question.ID, "yes"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-answerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed AskQuestion did not receive stored answer")
	}
	answer, err := store.AskQuestion(context.Background(), "question-task", question)
	if err != nil || answer != "yes" {
		t.Fatalf("replayed answered question = %q, %v", answer, err)
	}
}

func TestCancelQuestionNeverCreatesAnAnswer(t *testing.T) {
	store := questionTestStore(t)
	q := Question{ID: "call-cancel", SessionID: "session-1", Text: "Proceed?"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := store.AskQuestion(ctx, "question-task", q); result <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		pending, _ := store.ListQuestions("question-task")
		if len(pending) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("question never became pending")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := store.CancelQuestion("question-task", q.ID); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrQuestionCanceled) {
		t.Fatalf("canceled question returned %v", err)
	}
	if err := store.AnswerQuestion("question-task", q.ID, "yes"); !errors.Is(err, ErrQuestionCanceled) {
		t.Fatalf("answer after cancellation returned %v", err)
	}
	dir := filepath.Join(store.Root, "question-task")
	removeQuestionEvents(t, dir, q.ID, "task_question_cancelled")
	if err := store.CancelQuestion("question-task", q.ID); err != nil {
		t.Fatalf("cancel retry did not repair its event: %v", err)
	}
	waitForQuestionEvent(t, dir, q.ID, "task_question_cancelled")
}

func TestTerminalTaskRejectsNewAnswerAndHidesPendingQuestion(t *testing.T) {
	store := questionTestStore(t)
	q := Question{ID: "call-terminal", SessionID: "session-1", Text: "Proceed?"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := store.AskQuestion(ctx, "question-task", q); done <- err }()
	waitForQuestion(t, store, q.ID)
	dir := filepath.Join(store.Root, "question-task")
	task, err := readTask(dir)
	if err != nil {
		t.Fatal(err)
	}
	task.Status = StatusSucceeded
	if err = writeTask(dir, task); err != nil {
		t.Fatal(err)
	}
	if err = store.AnswerQuestion("question-task", q.ID, "yes"); err == nil {
		t.Fatal("terminal task accepted a new answer")
	}
	if pending, err := store.ListQuestions("question-task"); err != nil || len(pending) != 0 {
		t.Fatalf("terminal task exposed pending question: %+v, %v", pending, err)
	}
	cancel()
	<-done
}

func TestQuestionEncodedEventIsBounded(t *testing.T) {
	store := questionTestStore(t)
	question := Question{ID: "control-heavy", SessionID: "session", Text: strings.Repeat("\x00", 12<<10)}
	if _, err := store.AskQuestion(context.Background(), "question-task", question); err == nil || !strings.Contains(err.Error(), "encoded task question") {
		t.Fatalf("control-heavy question error=%v, want encoded-size rejection", err)
	}
	if pending, err := store.ListQuestions("question-task"); err != nil || len(pending) != 0 {
		t.Fatalf("oversized question was persisted: %+v, %v", pending, err)
	}
}
