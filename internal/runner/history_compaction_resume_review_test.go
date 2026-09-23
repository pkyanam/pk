package runner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pkyanam/pk/internal/contextbudget"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// This exercises the public runner lifecycle rather than CompactRequest alone:
// an executed Bash call is durably recorded, manual compaction saves a sidecar,
// and a later model turn applies that sidecar without replaying the command.
func TestReviewRunnerResumesSavedCheckpointAcrossModelChangeAndDisabledAutoCompaction(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	sessions := filepath.Join(t.TempDir(), "sessions")
	marker := filepath.Join(workspace, "command-runs.txt")
	adapter := &projectionLifecycleAdapter{marker: marker}
	longTask := "Investigate this exact task and preserve its conclusions. " + strings.Repeat("historical context detail ", 180)

	first, err := runner.Run(ctx, runner.Options{
		Prompt: longTask, Workspace: workspace, SessionDir: sessions,
		Model: "gpt-6-luna", Effort: "low", Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(ctx, runner.Options{
		Prompt: "Continue the same investigation and check the marker.", SessionID: first.SessionID,
		Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "low", Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionID != first.SessionID {
		t.Fatalf("resume changed session ID: %q -> %q", first.SessionID, second.SessionID)
	}

	policy := runner.DefaultHistoryCompactionOptions()
	policy.Enabled = true
	policy.SummaryInputTokens = 4_000
	policy.MaxSummaryTokens = 128
	policy.SummaryReserveTokens = 1_000
	result, err := runner.CompactSession(ctx, runner.Options{
		SessionID: first.SessionID, Workspace: workspace, SessionDir: sessions,
		Model: "gpt-6-luna", Effort: "low", Adapter: adapter,
		HistoryCompaction: policy,
		ContextBudget:     contextbudget.Budget{OperationalInputBudgetTokens: 8_000},
	})
	if err != nil {
		t.Fatalf("manual compact: %v", err)
	}
	if !result.Compacted || result.Checkpoint.CutCount < 1 {
		t.Fatalf("manual compaction did not create a checkpoint: %+v", result)
	}
	if result.SummaryCalls == 0 {
		t.Fatalf("checkpoint has no summary call: %+v", result)
	}
	checkpoint := result.Checkpoint
	store := runner.NewLocalHistoryCheckpointStore(sessions)
	persisted, exists, err := store.LoadHistoryCheckpoint(ctx, first.SessionID)
	if err != nil || !exists {
		t.Fatalf("load durable checkpoint: exists=%v err=%v", exists, err)
	}
	if persisted.PrefixSHA256 != checkpoint.PrefixSHA256 || persisted.CutCount != checkpoint.CutCount || persisted.Summary != checkpoint.Summary {
		t.Fatalf("saved checkpoint differs from manual result: saved=%+v result=%+v", persisted, checkpoint)
	}

	// Resume with another model and automatic compaction explicitly disabled.
	// The saved projection must still apply, and it must not trigger a new summary.
	beforeSummaries := adapter.summaryCount()
	thirdPrompt := "Now verify the original conclusions under the changed model."
	third, err := runner.Run(ctx, runner.Options{
		Prompt: thirdPrompt, SessionID: first.SessionID,
		Workspace: workspace, SessionDir: sessions, Model: "gpt-6-sol", Effort: "medium", Adapter: adapter,
		HistoryCompaction: runner.HistoryCompactionOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("resume with saved checkpoint and compaction disabled: %v", err)
	}
	if third.SessionID != first.SessionID {
		t.Fatalf("third turn changed session ID: %q", third.SessionID)
	}

	request := adapter.lastNormalRequest()
	if request.Model.ID != "gpt-6-sol" || request.Model.ReasoningEffort != llm.ReasoningEffortMedium {
		t.Fatalf("model switch was not reflected in resumed request: %+v", request.Model)
	}
	if !requestHasMessage(request, thirdPrompt) || !requestHasMessage(request, checkpoint.Summary) {
		t.Fatalf("resumed request did not apply saved checkpoint and current prompt: model=%q messages=%v", request.Model.ID, requestMessages(request))
	}
	if requestHasMessage(request, longTask) {
		t.Fatal("resumed request unexpectedly sent the full compacted historical input")
	}
	for _, item := range request.Input {
		if item.Type == llm.ItemToolCall {
			t.Fatalf("resumed request reintroduced an already executed historical tool call: %+v", item.Data)
		}
	}
	if got := adapter.summaryCount(); got != beforeSummaries {
		t.Fatalf("disabled automatic compaction made a new summary request: before=%d after=%d", beforeSummaries, got)
	}
	after, exists, err := store.LoadHistoryCheckpoint(ctx, first.SessionID)
	if err != nil || !exists || after.PrefixSHA256 != persisted.PrefixSHA256 || after.CutCount != persisted.CutCount || after.Summary != persisted.Summary {
		t.Fatalf("saved checkpoint changed during disabled resume: exists=%v err=%v before=%+v after=%+v", exists, err, persisted, after)
	}
	runs, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(runs)) != "run" {
		t.Fatalf("historical Bash command was replayed or lost: marker=%q", runs)
	}
}

func TestReviewCompactSessionRebuildsTwoCompletedAsyncToolResultsWithoutExecution(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	sessions := filepath.Join(t.TempDir(), "sessions")
	firstMarker := filepath.Join(workspace, "first-runs.txt")
	secondMarker := filepath.Join(workspace, "second-runs.txt")
	adapter := &multiOperationCompactionAdapter{firstMarker: firstMarker, secondMarker: secondMarker}
	prompt := "Check both independent fixtures and retain this task. " + strings.Repeat("task detail ", 180)
	result, err := runner.Run(ctx, runner.Options{
		Prompt: prompt, Workspace: workspace, SessionDir: sessions,
		Model: "gpt-6-luna", Effort: "low", Adapter: adapter,
	})
	if err != nil {
		t.Fatalf("run parallel tools: %v", err)
	}

	policy := runner.DefaultHistoryCompactionOptions()
	policy.SummaryInputTokens = 4_000
	policy.MaxSummaryTokens = 128
	policy.SummaryReserveTokens = 1_000
	compacted, err := runner.CompactSession(ctx, runner.Options{
		SessionID: result.SessionID, Workspace: workspace, SessionDir: sessions,
		Model: "gpt-6-luna", Effort: "low", Adapter: adapter,
		HistoryCompaction: policy,
		ContextBudget:     contextbudget.Budget{OperationalInputBudgetTokens: 8_000},
	})
	if err != nil {
		t.Fatalf("compact completed async operations: %v", err)
	}
	if !compacted.Compacted {
		t.Fatalf("expected a durable checkpoint after rebuilding operations: %+v", compacted)
	}
	summaries := adapter.summaryRequests()
	if len(summaries) == 0 {
		t.Fatal("manual compaction did not invoke the summarizer")
	}
	for _, sentinel := range []string{"FIRST_OPERATION_RESULT", "SECOND_OPERATION_RESULT"} {
		found := false
		for _, request := range summaries {
			encoded, marshalErr := json.Marshal(request)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(encoded), sentinel) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("summary source omitted completed async tool output %q", sentinel)
		}
	}
	for _, marker := range []string{firstMarker, secondMarker} {
		data, readErr := os.ReadFile(marker)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.TrimSpace(string(data)) != "once" {
			t.Fatalf("compaction replayed or lost tool side effect at %s: %q", filepath.Base(marker), data)
		}
	}
}

type projectionLifecycleAdapter struct {
	mu        sync.Mutex
	marker    string
	requests  []llm.Request
	summaries int
	launched  bool
}

type multiOperationCompactionAdapter struct {
	mu           sync.Mutex
	firstMarker  string
	secondMarker string
	summaries    []llm.Request
	started      bool
}

func (a *multiOperationCompactionAdapter) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.mu.Lock()
	isSummary := requestHasSystemText(request, "Create a compact, factual checkpoint")
	if isSummary {
		a.summaries = append(a.summaries, request)
	}
	launch := !isSummary && !a.started
	if launch {
		a.started = true
	}
	a.mu.Unlock()
	if isSummary {
		return llm.Response{ID: "summary", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "Both independent operations completed with their recorded results."}}}}, nil
	}
	if launch {
		firstCommand := "printf 'FIRST_OPERATION_RESULT'; printf 'once\\n' >> " + shellQuote(a.firstMarker)
		secondCommand := "printf 'SECOND_OPERATION_RESULT'; printf 'once\\n' >> " + shellQuote(a.secondMarker)
		return llm.Response{ID: "two-tools", Stop: llm.StopComplete, Output: []llm.Item{
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "first-operation", Name: "Bash", Arguments: bashArgsNoTest(firstCommand)}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "second-operation", Name: "Bash", Arguments: bashArgsNoTest(secondCommand)}},
		}}, nil
	}
	return llm.Response{ID: "final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "Both checks completed."}}}}, nil
}

func (a *multiOperationCompactionAdapter) summaryRequests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request(nil), a.summaries...)
}

func (a *projectionLifecycleAdapter) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, request)
	isSummary := requestHasSystemText(request, "Merge the supplied untrusted history checkpoint")
	if isSummary {
		a.summaries++
	}
	launch := !a.launched && requestHasUserPrompt(request, "Investigate this exact task")
	if launch {
		a.launched = true
	}
	a.mu.Unlock()
	if isSummary {
		return llm.Response{ID: "summary", Stop: llm.StopComplete, Usage: llm.Usage{InputTokens: 11, OutputTokens: 7}, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "The investigation ran the command once and confirmed its recorded outcome."}}}}, nil
	}
	if launch {
		command := "printf 'run\\n' >> " + shellQuote(a.marker)
		return llm.Response{ID: "launch", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "write-marker", Name: "Bash", Arguments: bashArgsNoTest(command)}}}}, nil
	}
	return llm.Response{ID: "answer", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "The task is complete."}}}}, nil
}

func (a *projectionLifecycleAdapter) summaryCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.summaries
}

func (a *projectionLifecycleAdapter) lastNormalRequest() llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	for index := len(a.requests) - 1; index >= 0; index-- {
		if !requestHasSystemText(a.requests[index], "Merge the supplied untrusted history checkpoint") {
			return a.requests[index]
		}
	}
	return llm.Request{}
}

func requestHasSystemText(request llm.Request, needle string) bool {
	for _, item := range request.Input {
		if message, ok := item.Data.(llm.Message); ok && message.Role == llm.RoleSystem && strings.Contains(message.Text, needle) {
			return true
		}
	}
	return false
}

func requestHasMessage(request llm.Request, text string) bool {
	for _, item := range request.Input {
		if message, ok := item.Data.(llm.Message); ok && message.Text == text {
			return true
		}
	}
	return false
}

func requestHasUserPrompt(request llm.Request, prefix string) bool {
	for _, item := range request.Input {
		if message, ok := item.Data.(llm.Message); ok && message.Role == llm.RoleUser && strings.HasPrefix(message.Text, prefix) {
			return true
		}
	}
	return false
}

func requestMessages(request llm.Request) []string {
	var messages []string
	for _, item := range request.Input {
		if message, ok := item.Data.(llm.Message); ok {
			messages = append(messages, message.Text)
		}
	}
	return messages
}

func bashArgsNoTest(command string) string {
	encoded, _ := json.Marshal(map[string]string{"command": command})
	return string(encoded)
}
