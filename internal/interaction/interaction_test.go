package interaction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type captureToolContext struct{ spec operation.Spec }

func (c *captureToolContext) Submit(spec operation.Spec) operation.ID {
	c.spec = spec
	return "value-result"
}

func TestDecorateRegistryOnlyAddsForegroundQuestionTool(t *testing.T) {
	base := tool.NewRegistry(tool.StaticTranslators{}, tool.BashName)
	if got := DecorateRegistry(base, nil); got != base {
		t.Fatal("nil broker should leave registry unchanged")
	}
	for _, def := range base.StaticDefinitions() {
		if def.Tool.Name == AskUserName {
			t.Fatal("headless registry advertises AskUser")
		}
	}
	decorated := DecorateRegistry(base, NewBroker(context.Background(), "session-1"))
	if len(decorated.StaticDefinitions()) != len(base.StaticDefinitions())+1 {
		t.Fatal("expected one additional definition")
	}
	if _, ok := decorated.Resolve(AskUserName); !ok {
		t.Fatal("AskUser translator is missing")
	}
	if _, ok := decorated.Resolve(tool.BashName); !ok {
		t.Fatal("base translator was not delegated")
	}
}

func TestAskUserCarriesQuestionAndReturnsActualAnswer(t *testing.T) {
	broker := NewBroker(context.Background(), "session-1")
	defer broker.Close()
	base := tool.NewRegistry(tool.StaticTranslators{})
	translator, ok := DecorateRegistry(base, broker).Resolve(AskUserName)
	if !ok {
		t.Fatal("AskUser translator missing")
	}
	result := make(chan tool.CallStatus, 1)
	callContext := &captureToolContext{}
	go func() {
		result <- translator.Translate(callContext, llm.ToolCall{CallID: "call-42", Arguments: `{"question":"Which language?","choices":["Go","Rust"]}`})
	}()
	select {
	case q := <-broker.Questions():
		if q.ID != "call-42" || q.Text != "Which language?" || q.SessionID != "session-1" || len(q.Choices) != 2 || q.Kind != "question" {
			t.Fatalf("unexpected question: %#v", q)
		}
		if err := broker.Answer(q.ID, "Go"); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("question was not published")
	}
	status := <-result
	if status.Error != "" {
		t.Fatalf("successful answer marked tool call failed: %q", status.Error)
	}
	if len(status.WaitingFor) != 1 || callContext.spec.Type != operation.TypeValue {
		t.Fatalf("answer should be a durable value operation: status=%#v spec=%#v", status, callContext.spec)
	}
	resultOperation := operation.Operation{ID: status.WaitingFor[0], Type: callContext.spec.Type, Version: callContext.spec.Version, State: callContext.spec.State, Status: operation.StatusCompleted}
	llmResult, err := translator.TranslateResult("call-42", status, []operation.Operation{resultOperation})
	if err != nil {
		t.Fatal(err)
	}
	if len(llmResult.Output) != 1 || llmResult.Output[0].Value != "Go" {
		t.Fatalf("unexpected tool result: %#v", llmResult)
	}
}

func TestConfirmationDefaultsAndCancelDoesNotAnswer(t *testing.T) {
	broker := NewBroker(context.Background(), "s")
	base := tool.NewRegistry(tool.StaticTranslators{})
	tr, _ := DecorateRegistry(base, broker).Resolve(AskUserName)
	finished := make(chan tool.CallStatus, 1)
	callContext := &captureToolContext{}
	go func() {
		finished <- tr.Translate(callContext, llm.ToolCall{CallID: "confirm", Arguments: `{"question":"Proceed?","kind":"confirmation"}`})
	}()
	q := <-broker.Questions()
	if q.Kind != "confirmation" || len(q.Choices) != 2 || q.Choices[0] != "Yes" || q.Choices[1] != "No" {
		t.Fatalf("confirmation defaults: %#v", q)
	}
	if err := broker.Cancel(q.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-broker.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("Cancel did not cancel run context")
	}
	status := <-finished
	if status.Error == "Yes" || status.Error == "No" {
		t.Fatal("cancel produced a synthetic answer")
	}
	if err := broker.Answer(q.ID, "Yes"); !errors.Is(err, ErrQuestionNotFound) {
		t.Fatalf("answer after cancellation = %v", err)
	}
	broker.Close()
}

func TestAnswerRejectsBlankAndStaleIDs(t *testing.T) {
	b := NewBroker(context.Background(), "s")
	defer b.Close()
	if err := b.Answer("missing", "yes"); !errors.Is(err, ErrQuestionNotFound) {
		t.Fatalf("stale answer = %v", err)
	}
}
