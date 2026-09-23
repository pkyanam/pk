package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParsePKUsagePreservesAvailabilityAndSession(t *testing.T) {
	output := "" +
		`{"type":"session","session_id":"run-1"}` + "\n" +
		`{"type":"usage","session_id":"run-1","response_id":"r1","input_tokens":100,"output_tokens":8,"cached_input_tokens":0,"cached_input_tokens_available":true,"cache_write_input_tokens":64,"cache_write_input_tokens_available":true,"usage_available":true}`
	got := parseOutput("pk", []byte(output))
	if got.session != "run-1" || got.responses != 1 || got.input != 100 || got.output != 8 || got.cached != 0 || got.writes != 64 {
		t.Fatalf("parsed usage = %#v", got)
	}
	if !got.inputAvailable || !got.outputAvailable || !got.cachedAvailable || !got.writesAvailable {
		t.Fatalf("usage availability = input:%t output:%t cached:%t writes:%t", got.inputAvailable, got.outputAvailable, got.cachedAvailable, got.writesAvailable)
	}
}

func TestReadContextMetricsRequiresSafeShapeAndCorrelation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	valid := `{"type":"benchmark_context","request_ordinal":1,"response_id":"r1","request_failed":false,"system_prompt":{"items":1,"bytes":10},"tool_schemas":{"items":0,"bytes":0},"message_roles":{"system":{"items":1,"bytes":10}},"tool_calls":{"items":0,"bytes":0},"tool_results":{"items":0,"bytes":0},"other_input":{"items":0,"bytes":0},"input_items":1,"input_value_bytes":10,"usage":{"input_tokens":5,"input_tokens_available":true,"output_tokens":2,"output_tokens_available":true,"cached_input_tokens":0,"cached_input_tokens_available":true,"cache_write_input_tokens":0,"cache_write_input_tokens_available":false}}`
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(valid + "\n")
	if records, available, err := readContextMetrics(path, []string{"r1"}, 1); err != nil || !available || len(records) != 1 {
		t.Fatalf("valid metrics: records=%d available=%t err=%v", len(records), available, err)
	}
	if _, available, err := readContextMetrics(path, []string{"different"}, 1); err != nil || available {
		t.Fatalf("mismatched response correlation: available=%t err=%v", available, err)
	}
	badRows := []string{
		strings.Replace(valid, `"bytes":10`, `"bytes":-10`, 1),
		strings.TrimSuffix(valid, "}") + `,"prompt":"PRIVATE_RAW_SENTINEL"}`,
		strings.Replace(valid, `"message_roles":{"system"`, `"message_roles":{"private"`, 1),
	}
	for _, bad := range badRows {
		write(bad + "\n")
		if _, _, err := readContextMetrics(path, []string{"r1"}, 1); err == nil {
			t.Fatalf("accepted malformed/raw metrics: %s", bad)
		}
	}
	write(strings.Replace(valid, `"request_ordinal":1`, `"request_ordinal":0`, 1) + "\n")
	if _, available, err := readContextMetrics(path, []string{"r1"}, 1); err != nil || available {
		t.Fatalf("invalid ordinal should mark unavailable: available=%t err=%v", available, err)
	}
	gap := strings.Replace(valid, `"request_ordinal":1`, `"request_ordinal":2`, 1)
	write(valid + "\n" + gap + "\n")
	if _, available, err := readContextMetrics(path, []string{"r1"}, 1); err != nil || available {
		t.Fatalf("ordinal gap/duplicate ID should mark unavailable: available=%t err=%v", available, err)
	}
}

func TestRunPhaseFakeChildCorrelatesMetricsAndStripsAmbientPolicy(t *testing.T) {
	root := t.TempDir()
	scriptPath := filepath.Join(root, "fake-pk")
	script := `#!/bin/sh
if [ "${PK_BENCH_POLICY+x}" = x ]; then exit 51; fi
if [ "${PK_BENCH_REPLAY_THRESHOLD_BYTES+x}" = x ]; then exit 52; fi
printf '%s\n' '{"type":"session","session_id":"s1"}' '{"type":"usage","response_id":"r1","input_tokens":5,"output_tokens":2,"usage_available":true}'
cat > "$PK_BENCH_CONTEXT_METRICS_FILE" <<'JSON'
{"type":"benchmark_context","request_ordinal":1,"response_id":"r1","request_failed":false,"system_prompt":{"items":1,"bytes":10},"tool_schemas":{"items":0,"bytes":0},"message_roles":{"system":{"items":1,"bytes":10}},"tool_calls":{"items":0,"bytes":0},"tool_results":{"items":0,"bytes":0},"other_input":{"items":0,"bytes":0},"input_items":1,"input_value_bytes":10,"usage":{"input_tokens":5,"input_tokens_available":true,"output_tokens":2,"output_tokens_available":true,"cached_input_tokens":0,"cached_input_tokens_available":false,"cache_write_input_tokens":0,"cache_write_input_tokens_available":false}}
JSON
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(root, "out")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_BENCH_POLICY", "output-cap-4k")
	t.Setenv("PK_BENCH_REPLAY_THRESHOLD_BYTES", "12345")
	got, err := runPhase(context.Background(), phaseOptions{
		engine: "pk", phase: "implementation", prompt: "fake", workspace: root, binary: scriptPath,
		pkHome: filepath.Join(root, "home"), outputDir: outputDir, taskName: "fixture", repetition: 1,
		timeout: 5 * time.Second, contextMetrics: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.ContextMetricsAvailable || got.ModelResponses != 1 || got.SessionID != "s1" {
		t.Fatalf("run record=%+v", got)
	}
	trace, err := os.ReadFile(filepath.Join(outputDir, "pk-rep1-fixture-implementation.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(trace, []byte(`"type":"benchmark_context"`)) || bytes.Contains(trace, []byte("PRIVATE_RAW_SENTINEL")) {
		t.Fatalf("unexpected trace: %s", trace)
	}
}

func TestSelectPilotTasksPreservesDefaultAndRequestedOrder(t *testing.T) {
	all, err := selectPilotTasks("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].name != "clamp" || all[1].name != "csvcount" || all[2].name != "intervals" {
		t.Fatalf("default pilot tasks = %#v", all)
	}
	selected, err := selectPilotTasks(" intervals, clamp ")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].name != "intervals" || selected[1].name != "clamp" {
		t.Fatalf("selected pilot tasks = %#v", selected)
	}
}

func TestSelectPilotTasksRejectsEmptyUnknownAndDuplicateNames(t *testing.T) {
	for _, selection := range []string{",", "clamp,", ",clamp", "unknown", "clamp,clamp"} {
		if _, err := selectPilotTasks(selection); err == nil {
			t.Errorf("selectPilotTasks(%q) succeeded, want error", selection)
		}
	}
}

func TestMatchedPilotSelectionIsRejectedForAblationsBeforeExecution(t *testing.T) {
	if got := run([]string{"-tasks", "clamp,intervals", "-output-cap-ablation"}); got != 2 {
		t.Fatalf("matched task selection with ablation = %d, want usage error 2", got)
	}
	if got := run([]string{"-total-timeout", "15m", "-tool-schema-ablation"}); got != 2 {
		t.Fatalf("matched whole-run timeout with ablation = %d, want usage error 2", got)
	}
	if got := run([]string{"-replay-compaction-profile", "aggressive"}); got != 2 {
		t.Fatalf("experimental replay profile without ablation = %d, want usage error 2", got)
	}
	if got := run([]string{"-replay-compaction-ablation", "-replay-compaction-profile", "unknown"}); got != 2 {
		t.Fatalf("unknown replay profile = %d, want usage error 2", got)
	}
}

func TestParseOutputCapCountersAndByteMetrics(t *testing.T) {
	output := `{"type":"benchmark_tool_limits","explicit_bash_output_limits":3,"omitted_bash_output_limits":2,"benchmark_default_applied":2,"bash_output_bytes":4096,"bash_error_bytes":21,"bash_raw_output_bytes":9000,"bash_raw_error_bytes":30,"bash_output_truncated_operations":1,"bash_error_truncated_operations":0,"bash_metrics_available":true}`
	got := parseOutput("pk-current", []byte(output))
	if !got.bashMetricsAvailable || got.explicitLimits != 3 || got.omittedLimits != 2 || got.defaultedLimits != 2 || got.bashOutputBytes != 4096 || got.bashErrorBytes != 21 || got.bashRawOutputBytes != 9000 || got.bashRawErrorBytes != 30 || got.bashOutputTruncated != 1 {
		t.Fatalf("benchmark metrics = %#v", got)
	}
}

func TestParseToolSchemaMetrics(t *testing.T) {
	output := `{"type":"benchmark_tool_schema","mode":"compact-tool-schema","tool_description_bytes_before":573,"tool_description_bytes_after":452,"tool_description_fields_changed":5,"tool_schema_metrics_available":true}`
	got := parseOutput("pk-compact-schema", []byte(output))
	if !got.toolSchemaMetricsAvailable || got.toolDescriptionBytesBefore != 573 || got.toolDescriptionBytesAfter != 452 || got.toolDescriptionFieldsChanged != 5 {
		t.Fatalf("tool schema metrics = %#v", got)
	}
}

func TestParseReplayCompactionMetrics(t *testing.T) {
	output := `{"type":"benchmark_context_compaction","context_compaction_metrics":{"eligible_bash_results":2,"compacted_bash_results":1,"original_result_bytes":17000,"stored_result_bytes":9000,"missing_capture_fallbacks":1},"context_compaction_metrics_available":true}`
	got := parseOutput("pk-compact-replayed-output", []byte(output))
	if !got.replayMetricsAvailable || got.replayEligibleResults != 2 || got.replayCompactedResults != 1 || got.replayOriginalBytes != 17000 || got.replayStoredBytes != 9000 || got.replayMissingCapture != 1 {
		t.Fatalf("context compaction metrics = %#v", got)
	}
}

func TestReplayCompactionSummaryReportsManipulationAndMeasuredUsage(t *testing.T) {
	passed := true
	result := suite{Model: "gpt-6-luna", Effort: "medium", Experiment: "replay compact", ReplayCompactionExperiment: true, ReplayCompactionProfile: "large-output", ReplayThresholdBytes: 16_384, ReplayExcerptRunes: 2_048, Records: []runRecord{{Engine: "pk-compact-replayed-output-large", Task: "fixture", Phase: "verification", ReplayMetricsAvailable: true, ReplayEligibleResults: 2, ReplayCompactedResults: 2, ReplayOriginalBytes: 36000, ReplayStoredBytes: 12000, CorrectnessPassed: &passed}}}
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := writeMarkdown(path, result); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Replay compaction profile: `large-output`", "Replay compaction threshold: 16384 bytes", "Replay head/tail excerpt: 2048 runes each", "configured 16384-byte threshold", "2048-rune head and tail", "Eligible / compacted", "2 / 2", "36000 / 12000", "provider-reported", "do not establish general task quality or cache savings"} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("summary missing %q:\n%s", expected, content)
		}
	}
}

func TestLargeReplayProfileMetadataIsPersisted(t *testing.T) {
	result := suite{
		ReplayCompactionExperiment: true,
		ReplayCompactionProfile:    "large-output",
		ReplayThresholdBytes:       16_384,
		ReplayExcerptRunes:         2_048,
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["replay_compaction_profile"] != "large-output" || got["replay_compaction_threshold_bytes"] != float64(16_384) || got["replay_compaction_excerpt_runes"] != float64(2_048) {
		t.Fatalf("profile metadata not recorded exactly: %s", data)
	}
}

func TestToolSchemaSummaryReportsProviderUsageAndDoesNotAssertSavings(t *testing.T) {
	passed := true
	result := suite{
		Model: "gpt-6-luna", Effort: "medium", Experiment: "Static tool-schema descriptions: current vs compact prose",
		ToolSchemaExperiment: true,
		Records:              []runRecord{{Engine: "pk-compact-schema", Task: "fixture", Phase: "verification", ToolSchemaMetricsAvailable: true, ToolDescriptionBytesBefore: 573, ToolDescriptionBytesAfter: 452, ToolDescriptionFieldsChanged: 5, CorrectnessPassed: &passed}},
	}
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := writeMarkdown(path, result); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, expected := range []string{"Description bytes (before / after)", "573 / 452", "provider-reported", "This paired experiment measures"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("summary missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "reduces measured usage") || strings.Contains(text, "token savings") {
		t.Fatalf("summary makes an unsupported savings claim:\n%s", text)
	}
}

func TestOutputCapFixtureStartsAsCleanGitWorktree(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := initializeFixtureGit(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	status := exec.Command("git", "status", "--porcelain")
	status.Dir = workspace
	if output, err := status.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("initial fixture git status = %q, %v", output, err)
	}
	diff := exec.Command("git", "diff", "--check")
	diff.Dir = workspace
	if output, err := diff.CombinedOutput(); err != nil {
		t.Fatalf("initial fixture git diff --check = %q, %v", output, err)
	}
}

func TestSessionModeUsesWhetherCallerSuppliedExistingSession(t *testing.T) {
	if got := sessionMode(false); got != "new_session" {
		t.Fatalf("new run mode = %q", got)
	}
	if got := sessionMode(true); got != "resumed_session" {
		t.Fatalf("resume mode = %q", got)
	}
}

func TestCancellationDescriptionDistinguishesBenchmarkShutdownAndPhaseTimeout(t *testing.T) {
	if got := cancellationDescription(context.Canceled, context.Canceled, "child failed"); got != "benchmark canceled: context canceled" {
		t.Fatalf("parent cancellation = %q", got)
	}
	if got := cancellationDescription(nil, context.DeadlineExceeded, "child failed"); got != "phase exceeded its configured timeout" {
		t.Fatalf("phase timeout = %q", got)
	}
	if got := cancellationDescription(nil, nil, "child failed"); got != "child failed" {
		t.Fatalf("ordinary failure = %q", got)
	}
}

func TestMissingProviderUsageRemainsUnknown(t *testing.T) {
	pkOutput := `{"type":"usage","response_id":"r1","input_tokens":0,"output_tokens":0,"cached_input_tokens":0,"cached_input_tokens_available":false,"cache_write_input_tokens":0,"cache_write_input_tokens_available":false,"usage_available":false}`
	pk := parseOutput("pk", []byte(pkOutput))
	if pk.inputAvailable || pk.outputAvailable || pk.cachedAvailable || pk.writesAvailable {
		t.Fatalf("pk missing usage was treated as known zero: %#v", pk)
	}
	unrealOutput := `{"Kind":"model_response","Data":{"Response":{"ID":"r1","Usage":{"InputTokens":0,"OutputTokens":0,"CachedInputTokens":0,"CacheWriteInputTokens":0,"Raw":null}}}}`
	unreal := parseOutput("unreal", []byte(unrealOutput))
	if unreal.inputAvailable || unreal.outputAvailable || unreal.cachedAvailable || unreal.writesAvailable {
		t.Fatalf("Unreal missing usage was treated as known zero: %#v", unreal)
	}
}

func TestHoldoutUsesOriginalTestsAndArchivesModelSource(t *testing.T) {
	fixture, workspace, output := t.TempDir(), t.TempDir(), t.TempDir()
	for path, data := range map[string]string{
		"go.mod":        "module holdout\n\ngo 1.27.0\n",
		"value.go":      "package holdout\nfunc Value() int { return 1 }\n",
		"value_test.go": "package holdout\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value() != 2 { t.Fatal(Value()) } }\n",
	} {
		if err := os.WriteFile(filepath.Join(fixture, path), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module holdout\n\ngo 1.27.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "value.go"), []byte("package holdout\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "value_test.go"), []byte("package holdout\nimport \"testing\"\nfunc TestValue(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	passed, err := verifyHoldout(context.Background(), fixture, workspace, output, "pk", 1, "sample")
	if passed || err == nil {
		t.Fatalf("holdout result = %t, %v; edited tests must not affect correctness", passed, err)
	}
	archived, err := os.ReadFile(filepath.Join(output, "artifacts", "pk-rep1-sample", "value.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(archived) != "package holdout\nfunc Value() int { return 1 }\n" {
		t.Fatalf("archived source = %q", archived)
	}
}

func TestParseUnrealUsageAndSanitizeRawEvents(t *testing.T) {
	line := `{"Sequence":3,"RecordedAt":"2026-09-22T00:00:00Z","Kind":"model_response","Data":{"Response":{"ID":"r1","Usage":{"InputTokens":100,"CachedInputTokens":0,"CacheWriteInputTokens":0,"OutputTokens":8,"Raw":{"input_tokens":100,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"access_token":"do-not-save"}}}}}`
	got := parseOutput("unreal", []byte(line))
	if got.responses != 1 || got.input != 100 || got.output != 8 || got.cached != 0 || got.writes != 0 {
		t.Fatalf("parsed usage = %#v", got)
	}
	if !got.inputAvailable || !got.outputAvailable || !got.cachedAvailable || !got.writesAvailable {
		t.Fatalf("usage availability = input:%t output:%t cached:%t writes:%t", got.inputAvailable, got.outputAvailable, got.cachedAvailable, got.writesAvailable)
	}
	encoded, err := json.Marshal(got.clean)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "do-not-save") || strings.Contains(string(encoded), "access_token") || strings.Contains(string(encoded), `"Raw"`) {
		t.Fatalf("sanitized event retained provider raw data: %s", encoded)
	}
}

func TestSanitizeErrorRedactsCredentialAssignments(t *testing.T) {
	got := sanitizeError("request failed access_token=abc123 authorization: Bearer xyz789")
	if strings.Contains(got, "abc123") || strings.Contains(got, "xyz789") {
		t.Fatalf("credential values were not redacted: %q", got)
	}
}

func TestUnrealStartupAcceptsPinnedCLIFlagsAndRequestWithoutNetwork(t *testing.T) {
	temp := t.TempDir()
	authPath := filepath.Join(temp, "auth.json")
	payload, err := json.Marshal(map[string]any{
		"exp":                         time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "benchmark-test-account"},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	authData := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"account_id":"benchmark-test-account"}}`, token)
	if err := os.WriteFile(authPath, []byte(authData), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(temp, "unreal-agent-runner")
	if err := build(context.Background(), ".", binary, "github.com/unreallabsai/unreal-agent/cmd/unreal-agent-runner"); err != nil {
		t.Fatal(err)
	}
	if err := checkUnrealStartup(context.Background(), binary, temp, authPath); err != nil {
		t.Fatalf("upstream command-line/request preflight failed (it must stop before any model request): %v", err)
	}
}
