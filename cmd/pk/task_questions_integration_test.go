//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/interaction"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/tasks"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// This subprocess fixture exercises the real Runner/AskUser translator and the
// same durable question bridge used by runTaskWorker.
func TestDurableTaskAskUserWorker(t *testing.T) {
	if os.Getenv("PK_TASK_ASKUSER_HELPER") != "1" {
		return
	}
	taskID := os.Getenv("PK_TASK_ID")
	err := tasks.RunWorker(context.Background(), taskID, func(ctx context.Context, worker tasks.WorkerOptions, output io.Writer) error {
		broker := interaction.NewBroker(ctx, worker.SessionID)
		var mu sync.RWMutex
		sessionID := worker.SessionID
		prior := worker.OnSession
		onSession := func(id string) {
			mu.Lock()
			sessionID = id
			mu.Unlock()
			if prior != nil {
				prior(id)
			}
		}
		done := bridgeTaskQuestions(broker, tasks.Store{Root: os.Getenv("PK_TASK_STORE")}, taskID, func() string {
			mu.RLock()
			defer mu.RUnlock()
			return sessionID
		})
		options := runner.Options{
			Prompt: worker.Prompt, PromptID: worker.PromptID, SessionID: worker.SessionID,
			Workspace: worker.Workspace, Model: worker.Model, Effort: worker.Effort,
			SessionDir: worker.SessionDir, Output: output, Adapter: askUserResumeAdapter{}, OnSession: onSession,
			DecorateRegistry: func(base tool.Registry) tool.Registry { return interaction.DecorateRegistry(base, broker) },
		}
		_, runErr := runner.Run(broker.Context(), options)
		broker.Close()
		<-done
		return runErr
	})
	if err != nil {
		os.Exit(3)
	}
}

type askUserResumeAdapter struct{}

func (askUserResumeAdapter) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	for _, item := range request.Input {
		if item.Type != llm.ItemToolResult {
			continue
		}
		result, ok := item.Data.(llm.ToolResult)
		if ok && result.CallID == "stable-task-question" {
			for _, output := range result.Output {
				if output.Kind == llm.ToolResultText && output.Value == "Yes" {
					return assistantTextResponse("answered", "answer received"), nil
				}
			}
			return llm.Response{}, fmt.Errorf("AskUser result did not contain the explicit answer: %#v", result.Output)
		}
	}
	args, _ := json.Marshal(map[string]any{"question": "May I continue?", "kind": "confirmation", "choices": []string{"Yes", "No"}})
	return llm.Response{ID: "ask-user", Stop: llm.StopComplete, Output: []llm.Item{{
		Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "stable-task-question", Name: interaction.AskUserName, Arguments: string(args)},
	}}}, nil
}

func (askUserResumeAdapter) Close() error { return nil }

func TestTaskAskUserSurvivesWorkerInterruptionAndResume(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	store := tasks.Store{Root: filepath.Join(root, "tasks")}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TASK_ASKUSER_TEST_BINARY", helper)
	script := filepath.Join(root, "worker.sh")
	body := "#!/bin/sh\nPK_TASK_ID=\"$2\" PK_TASK_ASKUSER_HELPER=1 exec \"$TASK_ASKUSER_TEST_BINARY\" -test.run=^TestDurableTaskAskUserWorker$\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	task, err := store.Start(context.Background(), tasks.StartOptions{
		ID: "ask-user-restart", Prompt: "start a guarded task", Workspace: workspace,
		Model: "gpt-6-luna", Effort: "medium", SessionDir: filepath.Join(root, "sessions"), Executable: script,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		current, getErr := store.Get(task.ID)
		if getErr != nil || current.Status != tasks.StatusRunning {
			return
		}
		process, findErr := os.FindProcess(current.PID)
		if findErr == nil {
			_ = process.Kill()
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			latest, statusErr := store.Get(task.ID)
			if statusErr == nil && latest.Status != tasks.StatusRunning {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Errorf("task worker %s remained running after cleanup kill", task.ID)
	})
	question := waitPendingTaskQuestion(t, store, task.ID)
	if question.SessionID == "" || question.ID != "stable-task-question" {
		t.Fatalf("initial question identity=%+v", question)
	}
	process, err := os.FindProcess(task.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitTaskStatus(t, store, task.ID, tasks.StatusInterrupted)
	if _, err := store.Resume(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	resumedQuestion := waitPendingTaskQuestion(t, store, task.ID)
	if !reflect.DeepEqual(resumedQuestion, question) {
		t.Fatalf("resumed question changed identity/content: before=%+v after=%+v", question, resumedQuestion)
	}
	t.Setenv("PK_HOME", root)
	sink := &rpcEventSink{events: make(chan []byte, 128)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, started: true, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "status-question", Type: "task_status", Payload: mustJSON(map[string]any{"task_id": task.ID})}, make(chan turnDone, 1))
	statusEvent := waitSteeringEvent(t, sink, "status-question", "task_status")
	statusQuestions := statusEvent.Payload.(map[string]any)["pending_questions"].([]any)
	if len(statusQuestions) != 1 {
		t.Fatalf("RPC task status pending questions=%#v", statusQuestions)
	}
	server.handle(rpcMessage{Version: 1, ID: "attach-question", Type: "task_attach", Payload: mustJSON(map[string]any{"task_id": task.ID})}, make(chan turnDone, 1))
	attached := waitSteeringEvent(t, sink, "attach-question", "task_attached")
	attachedQuestions := attached.Payload.(map[string]any)["pending_questions"].([]any)
	if len(attachedQuestions) != 1 {
		t.Fatalf("RPC task attach pending questions=%#v", attachedQuestions)
	}
	server.handle(rpcMessage{Version: 1, ID: "answer-question", Type: "task_question_answer", Payload: mustJSON(map[string]any{"task_id": task.ID, "question_id": question.ID, "answer": "Yes"})}, make(chan turnDone, 1))
	waitSteeringEvent(t, sink, "answer-question", "task_question_answered")
	waitSteeringEvent(t, sink, "attach-question", "task_question_answered")
	cliOut, cliErr := &strings.Builder{}, &strings.Builder{}
	if code := runTaskCommand(context.Background(), []string{"answer", task.ID, question.ID, "Yes"}, cliOut, cliErr); code != 0 {
		t.Fatalf("idempotent CLI answer code=%d out=%q err=%q", code, cliOut.String(), cliErr.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := store.Follow(ctx, task.ID, 0, io.Discard); err != nil {
		t.Fatal(err)
	}
	finished, err := store.Get(task.ID)
	if err != nil || finished.Status != tasks.StatusSucceeded {
		t.Fatalf("resumed task=%+v err=%v", finished, err)
	}
	var questions, answers int
	_, err = store.FollowEvents(ctx, task.ID, 0, io.Discard, func(event tasks.Event) {
		switch event.Type {
		case "task_question":
			if event.QuestionID == question.ID {
				questions++
			}
		case "task_question_answered":
			if event.QuestionID == question.ID {
				answers++
			}
		}
	})
	if err != nil || questions != 1 || answers != 1 {
		t.Fatalf("durable question lifecycle questions=%d answers=%d err=%v", questions, answers, err)
	}
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func waitPendingTaskQuestion(t *testing.T, store tasks.Store, taskID string) tasks.Question {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		questions, err := store.ListQuestions(taskID)
		if err == nil && len(questions) > 0 {
			return questions[0]
		}
		time.Sleep(25 * time.Millisecond)
	}
	task, _ := store.Get(taskID)
	log, _ := os.ReadFile(filepath.Join(store.Root, taskID, "worker.log"))
	t.Fatalf("task did not publish a pending question; task=%+v log=%q", task, strings.ToValidUTF8(string(log), "�"))
	return tasks.Question{}
}

func waitTaskStatus(t *testing.T, store tasks.Store, taskID string, status tasks.Status) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task, err := store.Get(taskID)
		if err == nil && task.Status == status {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	task, err := store.Get(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != status {
		t.Fatalf("task status=%s want %s (error %q)", task.Status, status, task.Error)
	}
}
