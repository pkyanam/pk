package interaction

import (
	"context"
	"errors"
	"fmt"
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

func TestAskUserRoutesSequentialFollowupsAsSeparateQuestions(t *testing.T) {
	broker := NewBroker(context.Background(), "session-followup")
	defer broker.Close()
	base := tool.NewRegistry(tool.StaticTranslators{})
	translator, ok := DecorateRegistry(base, broker).Resolve(AskUserName)
	if !ok {
		t.Fatal("AskUser translator missing")
	}

	for i, item := range []struct{ id, question, answer string }{
		{"followup-1", "Where are you going?", "Portland"},
		{"followup-2", "How many days?", "Three"},
	} {
		callContext := &captureToolContext{}
		finished := make(chan tool.CallStatus, 1)
		go func(item struct{ id, question, answer string }) {
			finished <- translator.Translate(callContext, llm.ToolCall{
				CallID: item.id, Arguments: fmt.Sprintf(`{"question":%q}`, item.question),
			})
		}(item)

		select {
		case question := <-broker.Questions():
			if question.ID != item.id || question.Text != item.question {
				t.Fatalf("follow-up %d = %#v, want id %q and text %q", i+1, question, item.id, item.question)
			}
			if err := broker.Answer(item.id, item.answer); err != nil {
				t.Fatalf("answer follow-up %d: %v", i+1, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("follow-up %d was not published", i+1)
		}
		if status := <-finished; status.Error != "" || len(status.WaitingFor) != 1 {
			t.Fatalf("follow-up %d did not complete through AskUser: %#v", i+1, status)
		}
	}
}

func TestAskUserDescriptionKeepsSingleQuestionSchemaAndRoutingRule(t *testing.T) {
	const want = "Ask only when the answer matters. Route each needed question, including follow-ups, through this tool—not prose. Make safe assumptions otherwise. This is not tool approval."
	if got := askUserDefinition.Tool.Description; got != want {
		t.Fatalf("AskUser description = %q, want %q", got, want)
	}
	parameters := askUserDefinition.Tool.Parameters
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("AskUser properties = %#v", parameters["properties"])
	}
	for _, key := range []string{"question", "choices", "kind"} {
		if _, exists := properties[key]; !exists {
			t.Errorf("backward-compatible single-question property %q missing", key)
		}
	}
	if got := askUserDefinition.Tool.Name; got != AskUserName {
		t.Fatalf("AskUser tool name changed: %q", got)
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
