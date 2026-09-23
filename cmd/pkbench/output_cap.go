package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pkyanam/pk/internal/auth"
)

var outputCapTasks = []task{
	{
		name:                 "clamp-control",
		implementationPrompt: "The supplied workspace is a self-contained Git fixture. Check only its root AGENTS.md if present; do not search parent directories. Implement ClampInt in clamp.go to clamp inclusively between lower and upper; swap reversed bounds. Do not run tests in this phase.",
		verificationPrompt:   "The supplied workspace is a self-contained Git fixture. Check only its root AGENTS.md if present; do not search parent directories. Run go test ./... in this workspace. If anything fails, fix the implementation and rerun the tests until they pass.",
	},
	{
		name:                 "noisyrepo",
		implementationPrompt: "The supplied workspace is a self-contained Git fixture. Check only its root AGENTS.md if present; do not search parent directories. Implement Select in catalog/catalog.go. Consider enabled sources only. A source matches when path equals Prefix or begins with Prefix followed by a slash; Prefix \"/\" matches absolute paths, and an empty Prefix never matches. Choose the matching source with the longest Prefix, then the greater Priority; preserve original slice order for a remaining tie. Do not mutate sources. Return false and a zero Source when none match. Do not run tests in this phase.",
		verificationPrompt:   "The supplied workspace is a self-contained Git fixture. Check only its root AGENTS.md if present; do not search parent directories. Run go test ./... in this workspace. If anything fails, fix the implementation and rerun the tests until they pass.",
	},
}

// Both tasks are multi-file coding fixtures with realistic, verbose table
// suites. The shared prompts deliberately request normal `go test -v` evidence;
// neither fixture pads output or changes max_output_length.
var replayCompactionTasks = []task{
	{
		name:                 "routematch",
		implementationPrompt: "This workspace is a self-contained Git fixture. Implement routing.Choose and its path-matching helper in routing/types.go and routing/match.go. Only enabled rules match; an empty prefix never matches; prefix '/' matches absolute paths; other prefixes match only an exact path or a path beginning with prefix plus '/'. Choose the longest matching prefix, then higher Priority; preserve input order for a remaining tie. Do not mutate rules. Return false and a zero Rule if no rule matches. Run `go test -v ./...` and fix any failures before you finish.",
		verificationPrompt:   "Resume this routing task. Run `go test -v ./...`; if any test fails, fix the implementation and rerun until all tests pass.",
	},
	{
		name:                 "eventmerge",
		implementationPrompt: "This workspace is a self-contained Git fixture. Implement journal.Merge and its event precedence helper in journal/event.go and journal/order.go. Return one event per key, choosing higher Revision first and then later At when revisions tie. For a complete tie, prefer the earliest event in existing, then incoming order. Keep Deleted events as tombstones. Return results sorted by Key and do not mutate either input. Run `go test -v ./...` and fix any failures before you finish.",
		verificationPrompt:   "Resume this journal task. Run `go test -v ./...`; if any test fails, fix the implementation and rerun until all tests pass.",
	},
}

var replayTaskSelection = map[string]task{
	"routematch": replayCompactionTasks[0],
	"eventmerge": replayCompactionTasks[1],
	"clamp": {
		name:                 "clamp",
		implementationPrompt: "This workspace is a self-contained Git fixture. Implement ClampInt in clamp.go to clamp inclusively between lower and upper; if the bounds are reversed, swap them. Preserve the input value when it is in range. Do not run tests during this implementation phase.",
		verificationPrompt:   "Resume this clamp task. Run go test -v ./... and fix the implementation if any test fails.",
	},
	"webhook": {
		name:                 "webhook",
		implementationPrompt: "This workspace is a self-contained Git fixture. Implement the signed, idempotent HTTP webhook receiver in event.go, signature.go, memory_store.go, and handler.go. Validate a nonblank event ID and type plus exactly one JSON object payload. Verify HMAC-SHA256 against the exact raw request body, accepting the lowercase hex digest with or without a sha256= prefix and comparing it in constant time. Accept POST only; limit bodies to MaxBodyBytes; reject malformed, trailing, or unknown JSON fields. Return 401 for an invalid signature, 400 for invalid event input, 413 for oversized bodies, and 503 when configuration or storage is unavailable. Atomically store only the first event for each ID, respect canceled contexts, and defensively copy payload bytes on store and read. Return JSON {\"accepted\":true,\"duplicate\":false} with 202 for first delivery and {\"accepted\":true,\"duplicate\":true} with 200 for duplicates. Do not expose storage errors. Do not run tests during this implementation phase.",
		verificationPrompt:   "Resume this webhook task. Run go test -v ./... and fix the implementation until all tests pass. Do not weaken or remove the benchmark tests.",
	},
}

type pairedPolicy struct {
	resultPrefix     string
	name             string
	currentMode      string
	treatmentEngine  string
	treatmentMode    string
	toolSchema       bool
	replayCompaction bool
	tasks            []task
}

func runOutputCapExperiment(repo, out string, repetitions int, phaseTimeout time.Duration) int {
	return runPairedPolicyExperiment(repo, out, repetitions, phaseTimeout, pairedPolicy{
		resultPrefix: "output-cap", name: "Bash omitted-output-limit default: current vs 4096 characters",
		currentMode: "current", treatmentEngine: "pk-output-cap-4k", treatmentMode: "output-cap-4k",
	})
}

func runToolSchemaExperiment(repo, out string, repetitions int, phaseTimeout time.Duration) int {
	return runPairedPolicyExperiment(repo, out, repetitions, phaseTimeout, pairedPolicy{
		resultPrefix: "tool-schema", name: "Static tool-schema descriptions: current vs compact prose",
		currentMode: "tool-schema-current", treatmentEngine: "pk-compact-schema", treatmentMode: "compact-tool-schema", toolSchema: true,
	})
}

func runReplayCompactionExperiment(repo, out string, repetitions int, phaseTimeout time.Duration, selection string) int {
	tasks, err := selectReplayTasks(selection)
	if err != nil {
		return fail(err)
	}
	return runPairedPolicyExperiment(repo, out, repetitions, phaseTimeout, pairedPolicy{
		resultPrefix: "replay-compaction", name: "Large Bash result context replay: current vs compact captured result",
		currentMode: "replay-compaction-current", treatmentEngine: "pk-compact-replayed-output", treatmentMode: "compact-replayed-shell-output",
		replayCompaction: true, tasks: tasks,
	})
}

func selectReplayTasks(selection string) ([]task, error) {
	if strings.TrimSpace(selection) == "" {
		return append([]task(nil), replayCompactionTasks...), nil
	}
	parts := strings.Split(selection, ",")
	selected := make([]task, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			return nil, fmt.Errorf("replay task selection contains an empty name")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("replay task %q was selected more than once", name)
		}
		current, ok := replayTaskSelection[name]
		if !ok {
			return nil, fmt.Errorf("unknown replay task %q (available: clamp, eventmerge, routematch, webhook)", name)
		}
		selected = append(selected, current)
		seen[name] = struct{}{}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("replay task selection must include at least one fixture")
	}
	return selected, nil
}

func runPairedPolicyExperiment(repo, out string, repetitions int, phaseTimeout time.Duration, policy pairedPolicy) int {
	repoPath, err := filepath.Abs(repo)
	if err != nil {
		return fail(err)
	}
	started := time.Now().UTC()
	resultDir := out
	if resultDir == "" {
		resultDir = filepath.Join(repoPath, "benchmarks", "results", policy.resultPrefix+"-"+started.Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(resultDir, 0o700); err != nil {
		return fail(err)
	}
	resultDir, err = filepath.Abs(resultDir)
	if err != nil {
		return fail(err)
	}
	processContext, stopSignals := signalNotifyContext()
	defer stopSignals()
	wholeTimeout := 15 * time.Minute
	if repetitions == 2 {
		wholeTimeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(processContext, wholeTimeout)
	defer cancel()
	if _, err := auth.Credentials(ctx); err != nil {
		return fail(fmt.Errorf("pk credentials are unavailable: %w", err))
	}
	tempRoot, err := os.MkdirTemp("", "pkbench-output-cap-*")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(tempRoot)
	pkBinary := filepath.Join(tempRoot, "pk-benchmark")
	buildCommand := exec.CommandContext(ctx, "go", "build", "-tags", "pkbench", "-o", pkBinary, "./cmd/pk")
	buildCommand.Dir = repoPath
	if output, err := buildCommand.CombinedOutput(); err != nil {
		return fail(fmt.Errorf("build pk benchmark binary: %s", sanitizeError(string(output))))
	}
	if err := os.Chmod(pkBinary, 0o700); err != nil {
		return fail(err)
	}
	authPath := pkAuthPath()
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
	source, err := writeSourceManifest(repoPath, resultDir)
	if err != nil {
		return fail(err)
	}
	metadata := suite{
		StartedAt: started, Model: modelID, Effort: effort, Repetitions: repetitions,
		Timeout: phaseTimeout.String(), GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		PKRevision: gitRevision(ctx, repoPath), Unreal: "not used (paired pk-only policy ablation)",
		Experiment: policy.name, ToolSchemaExperiment: policy.toolSchema, ReplayCompactionExperiment: policy.replayCompaction,
		SourceTreeSHA256: source.TreeSHA256, GitDiffSHA256: source.DiffSHA256,
	}
	checkpoint := func() error {
		metadata.FinishedAt = time.Now().UTC()
		return writeResults(resultDir, metadata)
	}
	for rep := 1; rep <= repetitions; rep++ {
		sourceTasks := outputCapTasks
		if len(policy.tasks) != 0 {
			sourceTasks = policy.tasks
		}
		tasks := append([]task(nil), sourceTasks...)
		if rep%2 == 0 {
			tasks[0], tasks[1] = tasks[1], tasks[0]
		}
		engines := []struct{ name, mode string }{{"pk-current", policy.currentMode}, {policy.treatmentEngine, policy.treatmentMode}}
		if rep%2 == 0 {
			engines[0], engines[1] = engines[1], engines[0]
		}
		for _, current := range tasks {
			workspace := filepath.Join(tempRoot, fmt.Sprintf("%s-rep%d-paired", current.name, rep))
			fixtureName := strings.TrimSuffix(current.name, "-control")
			fixture := filepath.Join(repoPath, "benchmarks", "tasks", fixtureName)
			for _, engine := range engines {
				if err := os.RemoveAll(workspace); err != nil {
					return fail(fmt.Errorf("reset paired workspace: %w", err))
				}
				if err := copyTree(fixture, workspace); err != nil {
					return fail(err)
				}
				if err := initializeFixtureGit(ctx, workspace); err != nil {
					return fail(err)
				}
				first, firstErr := runPhase(ctx, phaseOptions{
					engine: engine.name, phase: "implementation", prompt: current.implementationPrompt,
					workspace: workspace, timeout: phaseTimeout, binary: pkBinary, mode: engine.mode,
					pkHome: pkHome, outputDir: resultDir, skillsDir: emptySkills, taskName: current.name, repetition: rep,
				})
				metadata.Records = append(metadata.Records, first)
				if err := checkpoint(); err != nil {
					return fail(err)
				}
				if firstErr != nil {
					fmt.Fprintf(os.Stderr, "%s rep %d %s implementation: %v\n", engine.name, rep, current.name, firstErr)
					passed, testErr := verifyHoldout(ctx, fixture, workspace, resultDir, engine.name, rep, current.name)
					metadata.Records = append(metadata.Records, runRecord{Engine: engine.name, Task: current.name, Repetition: rep, Phase: "verification", ExitCode: 1, Error: "skipped because implementation phase failed", CorrectnessPassed: &passed})
					if err := checkpoint(); err != nil {
						return fail(err)
					}
					if testErr != nil {
						fmt.Fprintf(os.Stderr, "%s rep %d %s holdout: %v\n", engine.name, rep, current.name, testErr)
					}
					if ctx.Err() != nil {
						return fail(ctx.Err())
					}
					continue
				}
				second, secondErr := runPhase(ctx, phaseOptions{
					engine: engine.name, phase: "verification", prompt: current.verificationPrompt,
					workspace: workspace, sessionID: first.SessionID, timeout: phaseTimeout,
					binary: pkBinary, mode: engine.mode, pkHome: pkHome, outputDir: resultDir,
					skillsDir: emptySkills, taskName: current.name, repetition: rep,
				})
				passed, testErr := verifyHoldout(ctx, fixture, workspace, resultDir, engine.name, rep, current.name)
				second.CorrectnessPassed = &passed
				if err := checkpoint(); err != nil {
					return fail(err)
				}
				metadata.Records = append(metadata.Records, second)
				if err := checkpoint(); err != nil {
					return fail(err)
				}
				if ctx.Err() != nil {
					return fail(ctx.Err())
				}
				if secondErr != nil {
					fmt.Fprintf(os.Stderr, "%s rep %d %s verification: %v\n", engine.name, rep, current.name, secondErr)
				}
				if testErr != nil {
					fmt.Fprintf(os.Stderr, "%s rep %d %s holdout: %v\n", engine.name, rep, current.name, testErr)
				}
				fmt.Printf("%s rep=%d task=%s responses=%d/%d cached=%d/%d passed=%t defaulted=%d\n", engine.name, rep, current.name, first.ModelResponses, second.ModelResponses, first.CachedTokens, second.CachedTokens, passed, first.DefaultedLimits+second.DefaultedLimits)
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

func initializeFixtureGit(ctx context.Context, workspace string) error {
	commands := [][]string{
		{"init", "--quiet", "--initial-branch=main"},
		{"add", "--all"},
		{"-c", "user.name=pkbench", "-c", "user.email=pkbench@example.invalid", "commit", "--quiet", "-m", "benchmark fixture baseline"},
	}
	for _, args := range commands {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workspace
		cmd.Env = withEnv(os.Environ(), "GIT_AUTHOR_DATE", "2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE", "2000-01-01T00:00:00Z")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("initialize Git fixture (%s): %s", args[0], sanitizeError(string(output)))
		}
	}
	return nil
}

func signalNotifyContext() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
