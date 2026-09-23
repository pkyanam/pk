// Command pkbench runs a small, reproducible coding-task pilot against pk and
// the pinned Unreal v0.1.1 runner. It stores only sanitized event summaries.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/pkyanam/pk/internal/auth"
)

var modelID = "gpt-6-luna"
var effort = "medium"

type task struct {
	name, implementationPrompt, verificationPrompt string
}

var tasks = []task{
	{
		name:                 "clamp",
		implementationPrompt: "Implement ClampInt in clamp.go to clamp inclusively between lower and upper. If bounds are reversed, swap them. Do not run tests in this phase.",
		verificationPrompt:   "Run go test ./... in this workspace. If anything fails, fix the implementation and rerun the tests until they pass.",
	},
	{
		name:                 "csvcount",
		implementationPrompt: "Implement SumColumn in csvcount.go. Read a CSV header, find the named column, parse every value in that column as base-10 int64, and return the sum. Return an error for empty input, a missing column, malformed CSV, or a non-integer value. Use only the standard library. Do not run tests in this phase.",
		verificationPrompt:   "Run go test ./... in this workspace. If anything fails, fix the implementation and rerun the tests until they pass.",
	},
	{
		name:                 "intervals",
		implementationPrompt: "Implement Merge in intervals.go. Return intervals sorted by Start, merge overlapping or touching intervals, and return a non-nil empty slice for empty input. Do not mutate the input slice. Do not run tests in this phase.",
		verificationPrompt:   "Run go test ./... in this workspace. If anything fails, fix the implementation and rerun the tests until they pass.",
	},
}

type runRecord struct {
	Engine                       string `json:"engine"`
	Task                         string `json:"task"`
	Repetition                   int    `json:"repetition"`
	Phase                        string `json:"phase"`
	SessionMode                  string `json:"session_mode"`
	SessionID                    string `json:"session_id,omitempty"`
	WallMS                       int64  `json:"wall_ms"`
	ModelResponses               int    `json:"model_responses"`
	ExplicitLimits               int64  `json:"explicit_bash_output_limits"`
	OmittedLimits                int64  `json:"omitted_bash_output_limits"`
	DefaultedLimits              int64  `json:"benchmark_default_applied"`
	BashOutputBytes              int64  `json:"bash_output_bytes"`
	BashErrorBytes               int64  `json:"bash_error_bytes"`
	BashRawOutputBytes           int64  `json:"bash_raw_output_bytes"`
	BashRawErrorBytes            int64  `json:"bash_raw_error_bytes"`
	BashOutputTruncated          int64  `json:"bash_output_truncated_operations"`
	BashErrorTruncated           int64  `json:"bash_error_truncated_operations"`
	BashMetricsAvailable         bool   `json:"bash_metrics_available"`
	ToolDescriptionBytesBefore   int64  `json:"tool_description_bytes_before"`
	ToolDescriptionBytesAfter    int64  `json:"tool_description_bytes_after"`
	ToolDescriptionFieldsChanged int64  `json:"tool_description_fields_changed"`
	ToolSchemaMetricsAvailable   bool   `json:"tool_schema_metrics_available"`
	ReplayEligibleResults        int64  `json:"eligible_bash_results"`
	ReplayCompactedResults       int64  `json:"compacted_bash_results"`
	ReplayOriginalBytes          int64  `json:"original_result_bytes"`
	ReplayStoredBytes            int64  `json:"stored_result_bytes"`
	ReplayMissingCapture         int64  `json:"missing_capture_fallbacks"`
	ReplayMetricsAvailable       bool   `json:"context_compaction_metrics_available"`
	InputTokens                  int64  `json:"input_tokens"`
	InputTokensAvailable         bool   `json:"input_tokens_available"`
	UncachedInputTokens          int64  `json:"uncached_input_tokens"`
	UncachedInputAvailable       bool   `json:"uncached_input_tokens_available"`
	OutputTokens                 int64  `json:"output_tokens"`
	OutputTokensAvailable        bool   `json:"output_tokens_available"`
	CachedTokens                 int64  `json:"cached_input_tokens"`
	CachedTokensAvailable        bool   `json:"cached_input_tokens_available"`
	CacheWriteTokens             int64  `json:"cache_write_input_tokens"`
	CacheWriteAvailable          bool   `json:"cache_write_input_tokens_available"`
	ExitCode                     int    `json:"exit_code"`
	Error                        string `json:"error,omitempty"`
	CorrectnessPassed            *bool  `json:"correctness_passed,omitempty"`
	PrivateFailedCommands        string `json:"private_failed_commands,omitempty"`
}

type suite struct {
	StartedAt                  time.Time   `json:"started_at"`
	FinishedAt                 time.Time   `json:"finished_at"`
	Model                      string      `json:"model"`
	Effort                     string      `json:"effort"`
	Repetitions                int         `json:"repetitions"`
	Timeout                    string      `json:"per_phase_timeout"`
	GoVersion                  string      `json:"go_version"`
	GOOS                       string      `json:"goos"`
	GOARCH                     string      `json:"goarch"`
	PKRevision                 string      `json:"pk_revision"`
	Unreal                     string      `json:"upstream_version"`
	Experiment                 string      `json:"experiment,omitempty"`
	ToolSchemaExperiment       bool        `json:"tool_schema_experiment,omitempty"`
	ReplayCompactionExperiment bool        `json:"replay_compaction_experiment,omitempty"`
	SourceTreeSHA256           string      `json:"source_tree_sha256,omitempty"`
	GitDiffSHA256              string      `json:"git_diff_sha256,omitempty"`
	Records                    []runRecord `json:"runs"`
}

func main() { os.Exit(run(os.Args[1:])) }

func validEffort(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func run(args []string) int {
	flags := flag.NewFlagSet("pkbench", flag.ContinueOnError)
	repo := flags.String("repo", ".", "pk repository root")
	out := flags.String("out", "", "result directory (default: benchmarks/results/<timestamp>)")
	repetitions := flags.Int("repetitions", 1, "pilot repetitions (1 or 2)")
	timeout := flags.Duration("timeout", 90*time.Second, "hard timeout for each model phase")
	model := flags.String("model", "gpt-6-luna", "model ID")
	effortFlag := flags.String("effort", "medium", "reasoning effort")
	includeUnreal := flags.Bool("unreal", true, "also run the unchanged Unreal v0.1.1 CLI baseline")
	outputCapExperiment := flags.Bool("output-cap-ablation", false, "run the paired current-vs-4K-default Bash policy experiment only")
	toolSchemaExperiment := flags.Bool("tool-schema-ablation", false, "run the paired current-vs-compact tool-description experiment only")
	replayCompactionExperiment := flags.Bool("replay-compaction-ablation", false, "run the paired current-vs-captured-output context replay experiment only")
	replayTasks := flags.String("replay-tasks", "", "comma-separated replay-ablation fixtures (default: routematch,eventmerge)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *repetitions < 1 || *repetitions > 2 || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "usage: pkbench [-repo DIR] [-out DIR] [-repetitions 1|2] [-timeout 90s]")
		return 2
	}
	if strings.TrimSpace(*model) == "" || !validEffort(*effortFlag) {
		fmt.Fprintln(os.Stderr, "-model must not be empty and -effort must be low, medium, high, xhigh, or max")
		return 2
	}
	modelID, effort = strings.TrimSpace(*model), strings.ToLower(strings.TrimSpace(*effortFlag))
	if (*outputCapExperiment && *toolSchemaExperiment) || (*outputCapExperiment && *replayCompactionExperiment) || (*toolSchemaExperiment && *replayCompactionExperiment) {
		fmt.Fprintln(os.Stderr, "choose at most one benchmark ablation")
		return 2
	}
	if *replayTasks != "" && !*replayCompactionExperiment {
		fmt.Fprintln(os.Stderr, "-replay-tasks requires -replay-compaction-ablation")
		return 2
	}
	if *outputCapExperiment {
		return runOutputCapExperiment(*repo, *out, *repetitions, *timeout)
	}
	if *toolSchemaExperiment {
		return runToolSchemaExperiment(*repo, *out, *repetitions, *timeout)
	}
	if *replayCompactionExperiment {
		return runReplayCompactionExperiment(*repo, *out, *repetitions, *timeout, *replayTasks)
	}
	if flags.NArg() != 0 || *repetitions < 1 || *repetitions > 2 || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "usage: pkbench [-repo DIR] [-out DIR] [-repetitions 1|2] [-timeout 90s] [-unreal=true]")
		return 2
	}
	repoPath, err := filepath.Abs(*repo)
	if err != nil {
		return fail(err)
	}
	started := time.Now().UTC()
	resultDir := *out
	if resultDir == "" {
		resultDir = filepath.Join(repoPath, "benchmarks", "results", "pilot-"+started.Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(resultDir, 0o700); err != nil {
		return fail(fmt.Errorf("create results directory: %w", err))
	}
	resultDir, err = filepath.Abs(resultDir)
	if err != nil {
		return fail(err)
	}

	processContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(processContext, 20*time.Minute)
	defer cancel()
	// Read credentials using pk's normal resolver. The credential value is never
	// serialized or printed; the upstream process reads the same protected file.
	if _, err := auth.Credentials(ctx); err != nil {
		return fail(fmt.Errorf("pk credentials are unavailable: %w", err))
	}

	tempRoot, err := os.MkdirTemp("", "pkbench-*")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(tempRoot)
	pkBinary := filepath.Join(tempRoot, "pk")
	if err := build(ctx, repoPath, pkBinary, "./cmd/pk"); err != nil {
		return fail(err)
	}
	var unrealBinary string
	if *includeUnreal {
		unrealBinary = filepath.Join(tempRoot, "unreal-agent-runner")
		if err := build(ctx, repoPath, unrealBinary, "github.com/unreallabsai/unreal-agent/cmd/unreal-agent-runner"); err != nil {
			return fail(err)
		}
	}
	authPath := pkAuthPath()
	if *includeUnreal {
		if err := checkUnrealStartup(ctx, unrealBinary, tempRoot, authPath); err != nil {
			return fail(fmt.Errorf("verify upstream baseline startup without network: %w", err))
		}
	}
	pkHome := filepath.Join(tempRoot, "pk-home")
	if err := os.MkdirAll(pkHome, 0o700); err != nil {
		return fail(err)
	}
	if err := os.Symlink(authPath, filepath.Join(pkHome, "auth.json")); err != nil {
		return fail(fmt.Errorf("link pk auth file without copying it: %w", err))
	}
	emptySkills := filepath.Join(tempRoot, "empty-skills")
	if err := os.MkdirAll(emptySkills, 0o700); err != nil {
		return fail(err)
	}

	metadata := suite{
		StartedAt: started, Model: modelID, Effort: effort, Repetitions: *repetitions,
		Timeout: timeout.String(), GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		PKRevision: gitRevision(ctx, repoPath), Unreal: "v0.1.1",
	}
	checkpoint := func() error {
		metadata.FinishedAt = time.Now().UTC()
		return writeResults(resultDir, metadata)
	}
	engines := []string{"pk"}
	if *includeUnreal {
		engines = append(engines, "unreal-v0.1.1")
	}
	for rep := 1; rep <= *repetitions; rep++ {
		for _, current := range tasks {
			for _, engine := range engines {
				workspace := filepath.Join(tempRoot, fmt.Sprintf("%s-rep%d-%s", current.name, rep, strings.ReplaceAll(engine, "/", "-")))
				if err := copyTree(filepath.Join(repoPath, "benchmarks", "tasks", current.name), workspace); err != nil {
					return fail(err)
				}
				sessionID := ""
				first, firstErr := runPhase(ctx, phaseOptions{engine: engine, phase: "implementation", prompt: current.implementationPrompt, workspace: workspace, timeout: *timeout, binary: pkBinary, unreal: unrealBinary, pkHome: pkHome, authPath: authPath, outputDir: resultDir, skillsDir: emptySkills, taskName: current.name, repetition: rep})
				metadata.Records = append(metadata.Records, first)
				if err := checkpoint(); err != nil {
					return fail(err)
				}
				if firstErr != nil {
					fmt.Fprintf(os.Stderr, "%s rep %d %s implementation: %v\n", engine, rep, current.name, firstErr)
					passed, testErr := verifyHoldout(ctx, filepath.Join(repoPath, "benchmarks", "tasks", current.name), workspace, resultDir, engine, rep, current.name)
					metadata.Records = append(metadata.Records, runRecord{Engine: engine, Task: current.name, Repetition: rep, Phase: "verification", ExitCode: 1, Error: "skipped because implementation phase failed", CorrectnessPassed: &passed})
					if err := checkpoint(); err != nil {
						return fail(err)
					}
					if ctx.Err() != nil {
						return fail(ctx.Err())
					}
					if testErr != nil {
						fmt.Fprintf(os.Stderr, "%s rep %d %s correctness: %v\n", engine, rep, current.name, testErr)
					}
					fmt.Printf("%s rep=%d task=%s requests=%d cached=%d passed=%t\n", engine, rep, current.name, first.ModelResponses, first.CachedTokens, passed)
					continue
				}
				sessionID = first.SessionID
				second, secondErr := runPhase(ctx, phaseOptions{engine: engine, phase: "verification", prompt: current.verificationPrompt, workspace: workspace, sessionID: sessionID, timeout: *timeout, binary: pkBinary, unreal: unrealBinary, pkHome: pkHome, authPath: authPath, outputDir: resultDir, skillsDir: emptySkills, taskName: current.name, repetition: rep})
				metadata.Records = append(metadata.Records, second)
				if err := checkpoint(); err != nil {
					return fail(err)
				}
				passed, testErr := verifyHoldout(ctx, filepath.Join(repoPath, "benchmarks", "tasks", current.name), workspace, resultDir, engine, rep, current.name)
				second.CorrectnessPassed = &passed
				metadata.Records[len(metadata.Records)-1].CorrectnessPassed = &passed
				if err := checkpoint(); err != nil {
					return fail(err)
				}
				if ctx.Err() != nil {
					return fail(ctx.Err())
				}
				if secondErr != nil {
					fmt.Fprintf(os.Stderr, "%s rep %d %s verification: %v\n", engine, rep, current.name, secondErr)
				}
				if testErr != nil {
					fmt.Fprintf(os.Stderr, "%s rep %d %s correctness: %v\n", engine, rep, current.name, testErr)
				}
				fmt.Printf("%s rep=%d task=%s requests=%d/%d cached=%d/%d passed=%t\n", engine, rep, current.name, first.ModelResponses, second.ModelResponses, first.CachedTokens, second.CachedTokens, passed)
			}
		}
	}
	metadata.FinishedAt = time.Now().UTC()
	if err := writeResults(resultDir, metadata); err != nil {
		return fail(err)
	}
	fmt.Printf("Results: %s\n", resultDir)
	return 0
}

type phaseOptions struct {
	engine, phase, prompt, workspace, sessionID, binary, unreal, mode, pkHome, authPath, outputDir, skillsDir, taskName string
	repetition                                                                                                          int
	timeout                                                                                                             time.Duration
}

type usageSum struct {
	responses                                                                                                   int
	input, output, cached, writes                                                                               int64
	inputAvailable, outputAvailable, cachedAvailable, writesAvailable                                           bool
	explicitLimits, omittedLimits, defaultedLimits                                                              int64
	bashOutputBytes, bashErrorBytes, bashOutputTruncated, bashErrorTruncated                                    int64
	bashRawOutputBytes, bashRawErrorBytes                                                                       int64
	bashMetricsAvailable                                                                                        bool
	toolDescriptionBytesBefore, toolDescriptionBytesAfter                                                       int64
	toolDescriptionFieldsChanged                                                                                int64
	toolSchemaMetricsAvailable                                                                                  bool
	replayEligibleResults, replayCompactedResults, replayOriginalBytes, replayStoredBytes, replayMissingCapture int64
	replayMetricsAvailable                                                                                      bool
	session                                                                                                     string
	clean                                                                                                       []map[string]any
}

func runPhase(parent context.Context, options phaseOptions) (runRecord, error) {
	ctx, cancel := context.WithTimeout(parent, options.timeout)
	defer cancel()
	resumingSession := options.sessionID != ""
	started := time.Now()
	var stdout, stderr bytes.Buffer
	var cmd *exec.Cmd
	privateFailureLog := ""
	if strings.HasPrefix(options.engine, "pk-") {
		failedLog, err := failedCommandLogPath(options.outputDir, options.engine, options.repetition, options.taskName, options.phase)
		if err != nil {
			return runRecord{}, err
		}
		privateFailureLog = failedLog
		args := []string{"run", "-p", options.prompt, "--workspace", options.workspace, "--model", modelID, "--effort", effort, "--jsonl", "--skills-dir", options.skillsDir}
		if options.sessionID != "" {
			args = append(args, "--session", options.sessionID)
		}
		cmd = exec.Command(options.binary, args...)
		cmd.Env = withEnv(os.Environ(), "PK_HOME", options.pkHome, "PK_BENCH_POLICY", options.mode, "PK_BENCH_FAILED_COMMAND_LOG", failedLog)
	} else if options.engine == "pk" {
		args := []string{"run", "-p", options.prompt, "--workspace", options.workspace, "--model", modelID, "--effort", effort, "--jsonl"}
		args = append(args, "--skills-dir", options.skillsDir)
		if options.sessionID != "" {
			args = append(args, "--session", options.sessionID)
		}
		cmd = exec.Command(options.binary, args...)
		cmd.Env = withEnv(os.Environ(), "PK_HOME", options.pkHome)
	} else {
		args := []string{"-workspace", options.workspace, "-session-directory", filepath.Join(filepath.Dir(options.workspace), "unreal-sessions", options.taskName+fmt.Sprintf("-%d", options.repetition))}
		cmd = exec.Command(options.unreal, args...)
		cmd.Env = withEnv(withoutEnv(os.Environ(), "OPENAI_CODEX_ACCESS_TOKEN", "OPENAI_CODEX_ACCOUNT_ID"), "UNREAL_HARNESS_LLM_PROVIDER", "openai-codex", "UNREAL_HARNESS_LLM_MODEL", modelID, "OPENAI_CODEX_AUTH_FILE", options.authPath)
		request := map[string]any{"prompt": options.prompt, "model": modelID, "thinking_level": effort, "max_attempts": 5}
		if options.sessionID != "" {
			request["session_id"] = options.sessionID
		} else {
			options.sessionID = newUUID()
			request["session_id"] = options.sessionID
		}
		encoded, _ := json.Marshal(request)
		cmd.Stdin = bytes.NewReader(encoded)
	}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	configureProcessGroup(cmd)
	err := runCommandContext(ctx, cmd)
	wall := time.Since(started)
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	usage := parseOutput(options.engine, stdout.Bytes())
	if usage.session == "" && options.sessionID != "" {
		usage.session = options.sessionID
	}
	rawName := fmt.Sprintf("%s-rep%d-%s-%s.jsonl", options.engine, options.repetition, options.taskName, options.phase)
	if err := writeJSONLines(filepath.Join(options.outputDir, rawName), usage.clean); err != nil {
		return runRecord{}, err
	}
	record := runRecord{
		Engine: options.engine, Task: options.taskName, Repetition: options.repetition, Phase: options.phase,
		SessionMode: sessionMode(resumingSession),
		SessionID:   usage.session, WallMS: wall.Milliseconds(), ModelResponses: usage.responses,
		ExplicitLimits: usage.explicitLimits, OmittedLimits: usage.omittedLimits, DefaultedLimits: usage.defaultedLimits,
		BashOutputBytes: usage.bashOutputBytes, BashErrorBytes: usage.bashErrorBytes,
		BashRawOutputBytes: usage.bashRawOutputBytes, BashRawErrorBytes: usage.bashRawErrorBytes,
		BashOutputTruncated: usage.bashOutputTruncated, BashErrorTruncated: usage.bashErrorTruncated,
		BashMetricsAvailable:         usage.bashMetricsAvailable,
		ToolDescriptionBytesBefore:   usage.toolDescriptionBytesBefore,
		ToolDescriptionBytesAfter:    usage.toolDescriptionBytesAfter,
		ToolDescriptionFieldsChanged: usage.toolDescriptionFieldsChanged,
		ToolSchemaMetricsAvailable:   usage.toolSchemaMetricsAvailable,
		ReplayEligibleResults:        usage.replayEligibleResults, ReplayCompactedResults: usage.replayCompactedResults,
		ReplayOriginalBytes: usage.replayOriginalBytes, ReplayStoredBytes: usage.replayStoredBytes,
		ReplayMissingCapture: usage.replayMissingCapture, ReplayMetricsAvailable: usage.replayMetricsAvailable,
		InputTokens: usage.input, InputTokensAvailable: usage.inputAvailable,
		UncachedInputTokens:    usage.input - usage.cached,
		UncachedInputAvailable: usage.inputAvailable && usage.cachedAvailable && usage.cached <= usage.input,
		OutputTokens:           usage.output, OutputTokensAvailable: usage.outputAvailable,
		CachedTokens: usage.cached, CachedTokensAvailable: usage.cachedAvailable,
		CacheWriteTokens: usage.writes, CacheWriteAvailable: usage.writesAvailable, ExitCode: exitCode,
	}
	if err != nil {
		record.Error = sanitizeError(err.Error() + " " + stderr.String())
		record.Error = cancellationDescription(parent.Err(), ctx.Err(), record.Error)
		setPrivateFailedCommandPath(&record, privateFailureLog, options.outputDir)
		return record, fmt.Errorf("%s", record.Error)
	}
	setPrivateFailedCommandPath(&record, privateFailureLog, options.outputDir)
	return record, nil
}

func cancellationDescription(parentErr, phaseErr error, fallback string) string {
	if parentErr != nil {
		return "benchmark canceled: " + parentErr.Error()
	}
	if errors.Is(phaseErr, context.DeadlineExceeded) {
		return "phase exceeded its configured timeout"
	}
	return fallback
}

func setPrivateFailedCommandPath(record *runRecord, path, outputDir string) {
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err == nil {
		record.PrivateFailedCommands = filepath.Join("~", ".local", "state", "pk", "benchmark-debug", filepath.Base(outputDir), filepath.Base(path))
	}
}

func sessionMode(resuming bool) string {
	if resuming {
		return "resumed_session"
	}
	return "new_session"
}

func failedCommandLogPath(outputDir, engine string, repetition int, taskName, phase string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(homeDir, ".local", "state", "pk", "benchmark-debug", filepath.Base(outputDir))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(root, fmt.Sprintf("%s-rep%d-%s-%s-failed-commands.jsonl", engine, repetition, taskName, phase)), nil
}

func parseOutput(engine string, output []byte) usageSum {
	var sum usageSum
	seenResponseIDs := make(map[string]struct{})
	for _, line := range bytes.Split(output, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event map[string]any
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if engine == "pk" || strings.HasPrefix(engine, "pk-") {
			typeName := stringValue(event["type"])
			if typeName == "session" {
				sum.session = stringValue(event["session_id"])
			}
			if typeName == "usage" || typeName == "assistant" {
				if responseID := stringValue(event["response_id"]); responseID != "" {
					if _, seen := seenResponseIDs[responseID]; !seen {
						sum.responses++
						seenResponseIDs[responseID] = struct{}{}
					}
				} else if typeName == "usage" {
					sum.responses++
				}
			}
			if typeName == "usage" {
				sumValue(&sum, event, "input_tokens", "inputAvailable", &sum.input)
				sumValue(&sum, event, "output_tokens", "outputAvailable", &sum.output)
				sumValue(&sum, event, "cached_input_tokens", "cached_input_tokens_available", &sum.cached)
				sumValue(&sum, event, "cache_write_input_tokens", "cache_write_input_tokens_available", &sum.writes)
				sum.inputAvailable = sum.inputAvailable || boolValue(event["usage_available"])
				sum.outputAvailable = sum.outputAvailable || boolValue(event["usage_available"])
			}
			if typeName == "benchmark_tool_limits" {
				sum.bashMetricsAvailable = true
				sum.explicitLimits += int64Value(event["explicit_bash_output_limits"])
				sum.omittedLimits += int64Value(event["omitted_bash_output_limits"])
				sum.defaultedLimits += int64Value(event["benchmark_default_applied"])
				sum.bashOutputBytes += int64Value(event["bash_output_bytes"])
				sum.bashErrorBytes += int64Value(event["bash_error_bytes"])
				sum.bashRawOutputBytes += int64Value(event["bash_raw_output_bytes"])
				sum.bashRawErrorBytes += int64Value(event["bash_raw_error_bytes"])
				sum.bashOutputTruncated += int64Value(event["bash_output_truncated_operations"])
				sum.bashErrorTruncated += int64Value(event["bash_error_truncated_operations"])
			}
			if typeName == "benchmark_tool_schema" {
				sum.toolSchemaMetricsAvailable = boolValue(event["tool_schema_metrics_available"])
				sum.toolDescriptionBytesBefore = int64Value(event["tool_description_bytes_before"])
				sum.toolDescriptionBytesAfter = int64Value(event["tool_description_bytes_after"])
				sum.toolDescriptionFieldsChanged = int64Value(event["tool_description_fields_changed"])
			}
			if typeName == "benchmark_context_compaction" {
				sum.replayMetricsAvailable = boolValue(event["context_compaction_metrics_available"])
				metrics := mapValue(event, "context_compaction_metrics")
				sum.replayEligibleResults += int64Value(metrics["eligible_bash_results"])
				sum.replayCompactedResults += int64Value(metrics["compacted_bash_results"])
				sum.replayOriginalBytes += int64Value(metrics["original_result_bytes"])
				sum.replayStoredBytes += int64Value(metrics["stored_result_bytes"])
				sum.replayMissingCapture += int64Value(metrics["missing_capture_fallbacks"])
			}
			sum.clean = append(sum.clean, sanitizePkEvent(event))
		} else {
			kind := stringValue(event["Kind"])
			data, _ := event["Data"].(map[string]any)
			if strings.EqualFold(kind, "model_response") {
				sum.responses++
				response := mapValue(data, "Response")
				usage := mapValue(response, "Usage")
				sumValue(&sum, usage, "InputTokens", "InputTokens", &sum.input)
				sumValue(&sum, usage, "OutputTokens", "OutputTokens", &sum.output)
				sumValue(&sum, usage, "CachedInputTokens", "CachedInputTokens", &sum.cached)
				sumValue(&sum, usage, "CacheWriteInputTokens", "CacheWriteInputTokens", &sum.writes)
				if raw, exists := lookupMap(usage, "Raw"); exists {
					rawBytes, _ := json.Marshal(raw)
					var details map[string]any
					if json.Unmarshal(rawBytes, &details) == nil {
						if _, ok := lookupMap(details, "input_tokens"); ok {
							sum.inputAvailable = true
						}
						if _, ok := lookupMap(details, "output_tokens"); ok {
							sum.outputAvailable = true
						}
						inputDetails := mapValue(details, "input_tokens_details")
						if _, ok := lookupMap(inputDetails, "cached_tokens"); ok {
							sum.cachedAvailable = true
						}
						if _, ok := lookupMap(inputDetails, "cache_write_tokens"); ok {
							sum.writesAvailable = true
						}
					}
				}
			}
			sum.clean = append(sum.clean, sanitizeUnrealEvent(event))
		}
	}
	return sum
}

func sumValue(sum *usageSum, event map[string]any, field, availability string, destination *int64) {
	if raw, ok := event[field]; ok {
		if value, ok := number(raw); ok {
			*destination += value
			if value != 0 {
				switch availability {
				case "inputAvailable", "InputTokens":
					sum.inputAvailable = true
				case "outputAvailable", "OutputTokens":
					sum.outputAvailable = true
				case "cached_input_tokens_available", "CachedInputTokens":
					sum.cachedAvailable = true
				case "cache_write_input_tokens_available", "CacheWriteInputTokens":
					sum.writesAvailable = true
				}
			}
		}
	}
	if boolValue(event[availability]) {
		switch availability {
		case "inputAvailable":
			sum.inputAvailable = true
		case "outputAvailable":
			sum.outputAvailable = true
		case "cached_input_tokens_available":
			sum.cachedAvailable = true
		case "cache_write_input_tokens_available":
			sum.writesAvailable = true
		}
	}
}

func sanitizePkEvent(event map[string]any) map[string]any {
	typ := stringValue(event["type"])
	out := map[string]any{"type": typ}
	for _, key := range []string{"session_id", "response_id", "model", "effort", "phase", "call_id", "name", "state", "elapsed_ms", "input_tokens", "output_tokens", "reasoning_tokens", "cached_input_tokens", "cached_input_tokens_available", "cache_write_input_tokens", "cache_write_input_tokens_available", "usage_available", "explicit_bash_output_limits", "omitted_bash_output_limits", "benchmark_default_applied", "bash_output_bytes", "bash_error_bytes", "bash_raw_output_bytes", "bash_raw_error_bytes", "bash_output_truncated_operations", "bash_error_truncated_operations", "bash_metrics_available", "tool_description_bytes_before", "tool_description_bytes_after", "tool_description_fields_changed", "tool_schema_metrics_available", "mode"} {
		if value, exists := event[key]; exists {
			out[key] = value
		}
	}
	if typ == "assistant" {
		if text, ok := event["text"].(string); ok {
			out["text_bytes"] = len(text)
		}
	}
	if typ == "tool_call" {
		for _, key := range []string{"arguments_preview", "command_preview", "error_excerpt"} {
			if value, exists := event[key]; exists {
				out[key] = sanitizeError(stringValue(value))
			}
		}
		if rawOperations, ok := event["operations"].([]any); ok {
			operations := make([]map[string]any, 0, len(rawOperations))
			for _, raw := range rawOperations {
				if operation, ok := raw.(map[string]any); ok {
					safe := map[string]any{}
					for _, key := range []string{"id", "type", "state", "exit_code", "output_excerpt", "error_excerpt"} {
						if value, exists := operation[key]; exists {
							if key == "output_excerpt" || key == "error_excerpt" {
								safe[key] = sanitizeError(stringValue(value))
							} else {
								safe[key] = value
							}
						}
					}
					operations = append(operations, safe)
				}
			}
			out["operations"] = operations
		}
	}
	return out
}

func int64Value(value any) int64 {
	if result, ok := number(value); ok {
		return result
	}
	return 0
}

func sanitizeUnrealEvent(event map[string]any) map[string]any {
	kind := stringValue(event["Kind"])
	out := map[string]any{"kind": kind}
	for _, key := range []string{"Sequence", "RecordedAt"} {
		if value, exists := event[key]; exists {
			out[strings.ToLower(key[:1])+key[1:]] = value
		}
	}
	if strings.EqualFold(kind, "model_response") {
		data, _ := event["Data"].(map[string]any)
		response := mapValue(data, "Response")
		usage := mapValue(response, "Usage")
		safeUsage := map[string]any{}
		for _, key := range []string{"InputTokens", "CachedInputTokens", "CacheWriteInputTokens", "OutputTokens", "ReasoningTokens"} {
			if value, exists := lookupMap(usage, key); exists {
				safeUsage[strings.ToLower(key[:1])+key[1:]] = value
			}
		}
		out["usage"] = safeUsage
		if id, exists := lookupMap(response, "ID"); exists {
			out["response_id"] = id
		}
	}
	return out
}

func correctness(ctx context.Context, workspace string) (bool, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "go", "test", "./...")
	cmd.Dir = workspace
	output, err := cmd.CombinedOutput()
	if err != nil {
		if checkCtx.Err() != nil {
			return false, errors.New("go test exceeded 60-second timeout")
		}
		return false, fmt.Errorf("go test failed: %s", sanitizeError(string(output)))
	}
	return true, nil
}

// verifyHoldout restores the benchmark-owned tests and overlays only model-
// produced production Go files. It also archives those files before the
// temporary worktree is removed so correctness claims remain auditable.
func verifyHoldout(ctx context.Context, fixture, workspace, outputDir, engine string, repetition int, taskName string) (bool, error) {
	audit, err := os.MkdirTemp("", "pkbench-holdout-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(audit)
	if err := copyTree(fixture, audit); err != nil {
		return false, err
	}
	artifactDir := filepath.Join(outputDir, "artifacts", fmt.Sprintf("%s-rep%d-%s", engine, repetition, taskName))
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return false, err
	}
	err = filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		auditPath := filepath.Join(audit, rel)
		if err := os.MkdirAll(filepath.Dir(auditPath), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(auditPath, data, 0o600); err != nil {
			return err
		}
		artifactPath := filepath.Join(artifactDir, rel+".txt")
		if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
			return err
		}
		return os.WriteFile(artifactPath, data, 0o600)
	})
	if err != nil {
		return false, err
	}
	return correctness(ctx, audit)
}

func build(ctx context.Context, repo, output, target string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", output, target)
	cmd.Dir = repo
	if data, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build %s: %s", target, sanitizeError(string(data)))
	}
	return nil
}

// checkUnrealStartup exercises its actual flag parser and strict JSON request
// decoder and provider construction, then expects a local invalid-session error.
// This proves the selected auth path is usable without making a network request.
func checkUnrealStartup(parent context.Context, binary, tempRoot, authPath string) error {
	workspace := filepath.Join(tempRoot, "startup-check-workspace")
	sessions := filepath.Join(tempRoot, "startup-check-sessions")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-workspace", workspace, "-session-directory", sessions)
	cmd.Env = withEnv(withoutEnv(os.Environ(), "OPENAI_CODEX_ACCESS_TOKEN", "OPENAI_CODEX_ACCOUNT_ID"), "UNREAL_HARNESS_LLM_PROVIDER", "openai-codex", "UNREAL_HARNESS_LLM_MODEL", modelID, "OPENAI_CODEX_AUTH_FILE", authPath)
	cmd.Stdin = strings.NewReader(`{"prompt":"startup validation only","model":"gpt-6-luna","thinking_level":"medium","max_attempts":1,"session_id":"invalid/session"}`)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return errors.New("runner unexpectedly succeeded with an invalid session ID")
	}
	var event map[string]any
	for _, line := range bytes.Split(output, []byte("\n")) {
		if json.Unmarshal(line, &event) == nil && stringValue(event["type"]) == "error" {
			break
		}
	}
	message := stringValue(event["message"])
	if !strings.Contains(message, "session ID") || strings.Contains(strings.ToLower(message), "auth") {
		return fmt.Errorf("CLI, request schema, or provider auth preflight failed before expected local session-ID rejection: %s", sanitizeError(string(output)))
	}
	return nil
}

func gitRevision(ctx context.Context, repo string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD")
	cmd.Dir = repo
	data, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm()&0o600)
	})
}

func writeResults(directory string, result suite) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "summary.json"), data, 0o600); err != nil {
		return err
	}
	return writeMarkdown(filepath.Join(directory, "summary.md"), result)
}

func writeMarkdown(path string, result suite) error {
	var out strings.Builder
	fmt.Fprintf(&out, "# pk coding-task pilot\n\n- Started: %s\n- Model: `%s` / `%s`\n- Repetitions: %d\n- Per-phase timeout: %s\n- pk source revision: `%s`\n- Upstream baseline: Unreal Agent %s\n- Runtime: %s %s/%s\n", result.StartedAt.Format(time.RFC3339), result.Model, result.Effort, result.Repetitions, result.Timeout, result.PKRevision, result.Unreal, result.GoVersion, result.GOOS, result.GOARCH)
	if result.Experiment != "" {
		fmt.Fprintf(&out, "- Experiment: %s\n- Source tree SHA-256: `%s`\n- Tracked diff SHA-256: `%s`\n", result.Experiment, result.SourceTreeSHA256, result.GitDiffSHA256)
	}
	fmt.Fprintln(&out)
	if result.Experiment != "" {
		if result.ReplayCompactionExperiment {
			fmt.Fprintln(&out, "Both arms use the production CLI, prompt, model and effort, empty skills, and identical Git-initialized task fixtures. The treatment compacts completed Bash result text over the 4096-byte threshold once, at translation, to a 768-rune head and tail, exact existing stdout/stderr capture paths, and exit code. It falls back to the original result if no capture file exists. The fixtures request ordinary verbose Go test output; they do not pad streams or modify tool output limits. Per-run eligible/compacted counts verify treatment exposure. Stored result bytes are a manipulation check only: provider-reported input, cached-input, output tokens and wall time are the efficiency measures. Holdout tests are restored from pristine fixtures after each run. Small stochastic results are descriptive and do not establish general task quality or cache savings.")
			fmt.Fprintln(&out, "\n| Engine | Task | Rep | Phase | Responses | Input | Uncached input | Output | Cached | Eligible / compacted | Result bytes (before / stored) | Missing captures | Wall ms | Correct |\n|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|:---:|")
		} else if result.ToolSchemaExperiment {
			fmt.Fprintln(&out, "Both arms use the product CLI, system prompt, model and effort, task prompts, empty skills, and matching Git-initialized fixtures. The treatment shortens selected static tool descriptions while preserving tool names, translators, parameter types, constraints, and defaults. UTF-8 description-byte metrics are measured from the actual registry definitions; provider-reported usage is the token measure. Input/output/cache counters come from each model response, with uncached input shown only when input and cached counts are available. Correctness is checked against pristine holdout tests, and generated production Go files are archived as `.go.txt` files. This paired experiment measures only these tasks and this description treatment.")
			fmt.Fprintln(&out, "\n| Engine | Task | Rep | Phase | Responses | Input | Uncached input | Output | Cached | Description bytes (before / after) | Fields changed | Wall ms | Correct |\n|---|---|---:|---|---:|---:|---:|---:|---:|---|---:|---:|:---:|")
		} else {
			fmt.Fprintln(&out, "Both arms use the product CLI, system prompt, model, task prompt, empty skills, and same fixture with a clean initial Git commit. The treatment changes the advertised Bash default to 4096 characters and injects that value only when a valid JSON Bash call omits `max_output_length`; explicit values pass through unchanged. Exact omission/default counts are recorded by a build-tagged benchmark hook, not inferred from truncated previews. Input/output/cache counters are provider-reported; `uncached_input_tokens` is input minus provider-reported cached tokens when both are available. Tool output byte totals and truncated-operation counts come from durable operation state. Commands are retained only for failed Bash operations in a separate private state directory; event excerpts are bounded and sanitized. Correctness is checked against pristine holdout tests, and generated production Go files are archived under `artifacts/` as `.go.txt` files. This small, stochastic paired test measures only these fixtures and this policy.")
			fmt.Fprintln(&out, "\n| Engine | Task | Rep | Phase | Responses | Input | Uncached input | Output | Cached | Bash caps (explicit / omitted / defaulted) | Returned bytes (out / err) | Raw bytes (out / err) | Truncated ops (out / err) | Wall ms | Correct |\n|---|---|---:|---|---:|---:|---:|---:|---:|---|---|---|---|---:|:---:|")
		}
	} else {
		fmt.Fprintln(&out, "The same fixtures and two prompts are used for each engine. Implementation creates a new session; verification resumes it and runs/fixes tests. These describe session boundaries only: either phase can include both cached and uncached provider tokens. Cached counts come from provider usage, not an inferred cold/warm classification. Response counts are observed model responses, not hidden HTTP retry attempts. Missing token fields stay marked unavailable; no dollar totals are estimated. The pk and upstream runners have different system prompts and tool/output presentation; results compare these real product configurations and do not isolate one code change. Correctness is checked against pristine fixture tests in a separate holdout directory, and model-produced production Go files are archived under `artifacts/` as `.go.txt` files.")
		fmt.Fprintln(&out, "\n| Engine | Task | Rep | Session mode | Phase | Responses | Input | Output | Cached | Cache write | Wall ms | Correct |\n|---|---|---:|---|---|---:|---:|---:|---:|---:|---:|:---:|")
	}
	records := append([]runRecord(nil), result.Records...)
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Task != records[j].Task {
			return records[i].Task < records[j].Task
		}
		if records[i].Repetition != records[j].Repetition {
			return records[i].Repetition < records[j].Repetition
		}
		if records[i].Engine != records[j].Engine {
			return records[i].Engine < records[j].Engine
		}
		return records[i].Phase < records[j].Phase
	})
	for _, record := range records {
		input, output, cached, writes := displayToken(record.InputTokens, record.InputTokensAvailable), displayToken(record.OutputTokens, record.OutputTokensAvailable), displayToken(record.CachedTokens, record.CachedTokensAvailable), displayToken(record.CacheWriteTokens, record.CacheWriteAvailable)
		correct := "—"
		if record.CorrectnessPassed != nil {
			correct = fmt.Sprint(*record.CorrectnessPassed)
		}
		if result.Experiment != "" {
			uncached := displayToken(record.UncachedInputTokens, record.UncachedInputAvailable)
			if result.ReplayCompactionExperiment {
				eligible, bytes, missing := "—", "—", "—"
				if record.ReplayMetricsAvailable {
					eligible = fmt.Sprintf("%d / %d", record.ReplayEligibleResults, record.ReplayCompactedResults)
					bytes = fmt.Sprintf("%d / %d", record.ReplayOriginalBytes, record.ReplayStoredBytes)
					missing = fmt.Sprint(record.ReplayMissingCapture)
				}
				fmt.Fprintf(&out, "| %s | %s | %d | %s | %d | %s | %s | %s | %s | %s | %s | %s | %d | %s |\n", record.Engine, record.Task, record.Repetition, record.Phase, record.ModelResponses, input, uncached, output, cached, eligible, bytes, missing, record.WallMS, correct)
			} else if result.ToolSchemaExperiment {
				descriptions, changed := "—", "—"
				if record.ToolSchemaMetricsAvailable {
					descriptions = fmt.Sprintf("%d / %d", record.ToolDescriptionBytesBefore, record.ToolDescriptionBytesAfter)
					changed = fmt.Sprint(record.ToolDescriptionFieldsChanged)
				}
				fmt.Fprintf(&out, "| %s | %s | %d | %s | %d | %s | %s | %s | %s | %s | %s | %d | %s |\n", record.Engine, record.Task, record.Repetition, record.Phase, record.ModelResponses, input, uncached, output, cached, descriptions, changed, record.WallMS, correct)
			} else {
				caps, outBytes, rawBytes, trunc := "—", "—", "—", "—"
				if record.BashMetricsAvailable {
					caps = fmt.Sprintf("%d / %d / %d", record.ExplicitLimits, record.OmittedLimits, record.DefaultedLimits)
					outBytes = fmt.Sprintf("%d / %d", record.BashOutputBytes, record.BashErrorBytes)
					rawBytes = fmt.Sprintf("%d / %d", record.BashRawOutputBytes, record.BashRawErrorBytes)
					trunc = fmt.Sprintf("%d / %d", record.BashOutputTruncated, record.BashErrorTruncated)
				}
				fmt.Fprintf(&out, "| %s | %s | %d | %s | %d | %s | %s | %s | %s | %s | %s | %s | %s | %d | %s |\n", record.Engine, record.Task, record.Repetition, record.Phase, record.ModelResponses, input, uncached, output, cached, caps, outBytes, rawBytes, trunc, record.WallMS, correct)
			}
		} else {
			fmt.Fprintf(&out, "| %s | %s | %d | %s | %s | %d | %s | %s | %s | %s | %d | %s |\n", record.Engine, record.Task, record.Repetition, record.SessionMode, record.Phase, record.ModelResponses, input, output, cached, writes, record.WallMS, correct)
		}
	}
	return os.WriteFile(path, []byte(out.String()), 0o600)
}

func displayToken(value int64, available bool) string {
	if !available {
		return "unknown"
	}
	return fmt.Sprint(value)
}
func pkAuthPath() string {
	if home := strings.TrimSpace(os.Getenv("PK_HOME")); home != "" {
		return filepath.Join(home, "auth.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pk", "auth.json")
}
func withEnv(environment []string, pairs ...string) []string {
	result := make([]string, 0, len(environment)+len(pairs)/2)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		replaced := false
		for i := 0; i+1 < len(pairs); i += 2 {
			if name == pairs[i] {
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, entry)
		}
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		result = append(result, pairs[i]+"="+pairs[i+1])
	}
	return result
}
func withoutEnv(environment []string, names ...string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		remove := false
		for _, blocked := range names {
			if name == blocked {
				remove = true
				break
			}
		}
		if !remove {
			result = append(result, entry)
		}
	}
	return result
}
func newUUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}
func lookupMap(value map[string]any, key string) (any, bool) {
	for current, item := range value {
		if strings.EqualFold(current, key) {
			return item, true
		}
	}
	return nil, false
}
func mapValue(value map[string]any, key string) map[string]any {
	result, _ := lookupMap(value, key)
	mapped, _ := result.(map[string]any)
	return mapped
}
func number(value any) (int64, bool) {
	switch current := value.(type) {
	case float64:
		return int64(current), true
	case json.Number:
		result, err := current.Int64()
		return result, err == nil
	case int64:
		return current, true
	}
	return 0, false
}
func boolValue(value any) bool     { current, _ := value.(bool); return current }
func stringValue(value any) string { current, _ := value.(string); return current }
func fail(err error) int           { fmt.Fprintln(os.Stderr, "pkbench:", sanitizeError(err.Error())); return 1 }
func sanitizeError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1500 {
		value = value[:1500]
	}
	value = bearerPattern.ReplaceAllString(value, "${1}[redacted]")
	return secretPattern.ReplaceAllString(value, "$1=[redacted]")
}
func writeJSONLines(path string, values []map[string]any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := json.NewEncoder(file)
	for _, value := range values {
		if err := writer.Encode(value); err != nil {
			return err
		}
	}
	return file.Sync()
}

var secretPattern = regexp.MustCompile(`(?i)(access_token|refresh_token|authorization|openai_api_key|password|secret|api[_-]?key)(\s*[:=]\s*)[^\s,"']+`)
var bearerPattern = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?)[^\s,"']+`)
