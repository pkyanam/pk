package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pkyanam/pk/internal/auth"
)

const subagentSchemaPrompt = `This workspace contains two independent Go modules: ./clamp and ./intervals. Delegate implementation to exactly two subagents; do not implement the changes yourself. Start both children before waiting, and assign one module to each. The clamp child owns clamp/clamp.go and implements ClampInt with inclusive bounds and swapping reversed bounds. The intervals child owns intervals/intervals.go and implements Merge: sort by Start, merge overlapping or touching ranges, return a non-nil empty slice for empty input, and do not mutate input. Children may edit only their assigned production .go file and may run go test; do not modify or weaken tests. After both children finish, summarize their results.`

const webhookSubagentSchemaPrompt = `This is a self-contained Go webhook fixture. Delegate implementation to exactly two subagents; do not implement the delegated files yourself. Start both children before waiting. Child 1 owns event.go and signature.go. Child 2 owns memory_store.go. You own and integrate handler.go after both children finish. Validate a nonblank event ID and type plus exactly one JSON object payload. Verify HMAC-SHA256 against the exact raw request body, accepting the lowercase hex digest with or without a sha256= prefix and comparing it in constant time. Accept POST only; limit bodies to MaxBodyBytes; reject malformed, trailing, or unknown JSON fields. Return 401 for an invalid signature, 400 for invalid event input, 413 for oversized bodies, and 503 when configuration or storage is unavailable. Atomically store only the first event for each ID, respect canceled contexts, and defensively copy payload bytes on store and read. Return JSON {"accepted":true,"duplicate":false} with 202 for first delivery and {"accepted":true,"duplicate":true} with 200 for duplicates. Do not expose storage errors. Children may edit only their assigned files. Do not modify or weaken tests, and do not run tests during implementation. The benchmark evaluator will run the pristine holdout tests after your final response.`

func parseSubagentTask(selection string) (string, error) {
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return "clamp+intervals", nil
	}
	if selection != "clamp+intervals" && selection != "webhook" {
		return "", fmt.Errorf("unknown subagent task %q (available: clamp+intervals, webhook)", selection)
	}
	return selection, nil
}

func resolveSubagentWholeTimeout(configured time.Duration, explicit bool, repetitions int) time.Duration {
	if explicit {
		return configured
	}
	if repetitions > 1 {
		return 30 * time.Minute
	}
	return 15 * time.Minute
}

func validSubagentTimeouts(phaseTimeout, wholeTimeout time.Duration) bool {
	return phaseTimeout > 0 && wholeTimeout > 0 && phaseTimeout <= 15*time.Minute && wholeTimeout <= 2*time.Hour
}

func subagentFixtureNames(taskName string) ([]string, error) {
	taskName, err := parseSubagentTask(taskName)
	if err != nil {
		return nil, err
	}
	if taskName == "webhook" {
		return []string{"webhook"}, nil
	}
	return []string{"clamp", "intervals"}, nil
}

func stageSubagentFixture(repo, workspace, taskName string) error {
	fixtureNames, err := subagentFixtureNames(taskName)
	if err != nil {
		return err
	}
	if taskName == "" {
		taskName = "clamp+intervals"
	}
	if taskName == "webhook" {
		return copyImplementationFixture(filepath.Join(repo, "benchmarks", "tasks", "webhook"), workspace)
	}
	for _, name := range fixtureNames {
		if err := copyImplementationFixture(filepath.Join(repo, "benchmarks", "tasks", name), filepath.Join(workspace, name)); err != nil {
			return err
		}
	}
	return nil
}

func subagentImplementationPrompt(taskName, model, modelEffort string) (string, error) {
	taskName, err := parseSubagentTask(taskName)
	if err != nil {
		return "", err
	}
	prompt := subagentSchemaPrompt
	if taskName == "webhook" {
		prompt = webhookSubagentSchemaPrompt
	}
	return prompt + fmt.Sprintf(" Use model %q and reasoning effort %q for both children.", model, modelEffort), nil
}

func validSubagentTaskCoverage(record runRecord, model, effort string) bool {
	if !validSubagentActionCoverage(record) || record.SubagentStartedChildren != 2 || record.SubagentCompletedChildren != 2 || record.SubagentChildrenWithUsage != 2 || !record.SubagentModelsAvailable || len(record.SubagentModels) != 2 || !record.CombinedResponsesAvailable || !record.CombinedInputAvailable || !record.CombinedOutputAvailable {
		return false
	}
	seen := make(map[string]bool, 2)
	for _, child := range record.SubagentModels {
		if child.ChildID == "" || seen[child.ChildID] || child.Model != model || child.Effort != effort {
			return false
		}
		seen[child.ChildID] = true
	}
	return len(seen) == 2
}

func subagentSchemaArms(repetition int) []struct{ name, mode string } {
	arms := []struct{ name, mode string }{{"pk-subagent-current", "subagent-schema-current"}, {"pk-subagent-compact", "compact-subagent-schema"}}
	if repetition%2 == 0 {
		arms[0], arms[1] = arms[1], arms[0]
	}
	return arms
}

func runSubagentSchemaExperiment(repo, out string, repetitions int, phaseTimeout, wholeTimeout time.Duration, model, modelEffort, taskName string) int {
	if !validSubagentTimeouts(phaseTimeout, wholeTimeout) {
		return fail(fmt.Errorf("subagent timeouts must be finite and within limits (phase <=15m, whole run <=2h)"))
	}
	parsedTask, err := parseSubagentTask(taskName)
	if err != nil {
		return fail(err)
	}
	taskName = parsedTask
	repoPath, err := filepath.Abs(repo)
	if err != nil {
		return fail(err)
	}
	started := time.Now().UTC()
	resultDir := out
	if resultDir == "" {
		resultDir = filepath.Join(repoPath, "benchmarks", "results", "subagent-schema-"+started.Format("20060102T150405Z"))
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
	ctx, cancel := context.WithTimeout(processContext, wholeTimeout)
	defer cancel()
	if _, err := auth.Credentials(ctx); err != nil {
		return fail(fmt.Errorf("pk credentials are unavailable: %w", err))
	}
	source, err := writeSourceManifest(repoPath, resultDir)
	if err != nil {
		return fail(err)
	}
	tempRoot, err := os.MkdirTemp("", "pkbench-subagent-schema-*")
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
	checkDir := filepath.Join(tempRoot, "source-check")
	if err := os.MkdirAll(checkDir, 0o700); err != nil {
		return fail(err)
	}
	verifiedSource, err := writeSourceManifest(repoPath, checkDir)
	if err != nil {
		return fail(err)
	}
	if source.TreeSHA256 != verifiedSource.TreeSHA256 || source.DiffSHA256 != verifiedSource.DiffSHA256 || source.PKRevision != verifiedSource.PKRevision {
		return fail(errors.New("benchmark source changed while building; refusing to run mismatched binary and source snapshot"))
	}
	pkHome := filepath.Join(tempRoot, "pk-home")
	if err := os.MkdirAll(pkHome, 0o700); err != nil {
		return fail(err)
	}
	if err := os.Symlink(pkAuthPath(), filepath.Join(pkHome, "auth.json")); err != nil {
		return fail(fmt.Errorf("link pk auth file without copying it: %w", err))
	}
	emptySkills := filepath.Join(tempRoot, "empty-skills")
	if err := os.MkdirAll(emptySkills, 0o700); err != nil {
		return fail(err)
	}
	taskSelection := []string{"clamp", "intervals"}
	if taskName == "webhook" {
		taskSelection = []string{"webhook"}
	}
	fixtureRoot := filepath.Join(resultDir, "source-snapshot", "files")
	metadata := suite{StartedAt: started, Model: model, Effort: modelEffort, Repetitions: repetitions, Timeout: phaseTimeout.String(), WholeTimeout: wholeTimeout.String(), GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, PKRevision: gitRevision(ctx, repoPath), Unreal: "not used (paired pk-only subagent schema ablation)", Experiment: "Subagent tool schema: five direct tools vs action dispatcher", SubagentSchemaExperiment: true, SourceTreeSHA256: source.TreeSHA256, GitDiffSHA256: source.DiffSHA256, TaskSelection: taskSelection}
	checkpoint := func() error { metadata.FinishedAt = time.Now().UTC(); return writeResults(resultDir, metadata) }
	failWithResults := func(err error) int {
		metadata.FinishedAt = time.Now().UTC()
		if writeErr := writeResults(resultDir, metadata); writeErr != nil {
			fmt.Fprintf(os.Stderr, "write partial results: %v\n", writeErr)
		}
		return fail(err)
	}
	for rep := 1; rep <= repetitions; rep++ {
		for _, arm := range subagentSchemaArms(rep) {
			workspace := filepath.Join(tempRoot, fmt.Sprintf("rep%d-%s", rep, arm.name))
			if err := os.MkdirAll(workspace, 0o700); err != nil {
				return failWithResults(err)
			}
			if err := stageSubagentFixture(fixtureRoot, workspace, taskName); err != nil {
				return failWithResults(err)
			}
			if err := initializeFixtureGit(ctx, workspace); err != nil {
				return failWithResults(err)
			}
			prompt, promptErr := subagentImplementationPrompt(taskName, model, modelEffort)
			if promptErr != nil {
				return failWithResults(promptErr)
			}
			record, runErr := runPhase(ctx, phaseOptions{engine: arm.name, phase: "delegation", prompt: prompt, effort: modelEffort, workspace: workspace, timeout: phaseTimeout, binary: pkBinary, mode: arm.mode, pkHome: pkHome, outputDir: resultDir, skillsDir: emptySkills, taskName: taskName, repetition: rep})
			if runErr != nil {
				record.Error = sanitizeError(runErr.Error())
			}
			passed := true
			var holdoutErrors []string
			holdoutNames := taskSelection
			for _, name := range holdoutNames {
				fixturePath := filepath.Join(fixtureRoot, "benchmarks", "tasks", name)
				workspacePath := workspace
				if taskName != "webhook" {
					workspacePath = filepath.Join(workspace, name)
				}
				passedHoldout, holdoutErr := verifyHoldout(ctx, fixturePath, workspacePath, resultDir, arm.name, rep, name)
				passed = passed && passedHoldout && holdoutErr == nil
				if holdoutErr != nil {
					holdoutErrors = append(holdoutErrors, name+" holdout: "+sanitizeError(holdoutErr.Error()))
				}
			}
			record.CorrectnessPassed = &passed
			coverageAvailable := subagentActionCoverageAvailable(record) && record.SubagentModelsAvailable && record.CombinedResponsesAvailable && record.CombinedInputAvailable && record.CombinedOutputAvailable
			record.SubagentActionCoverageAvailable = coverageAvailable
			record.SubagentActionCoverage = coverageAvailable && validSubagentTaskCoverage(record, model, modelEffort)
			if !record.SubagentActionCoverage {
				record.Error = strings.TrimSpace(record.Error + "; subagent action coverage not met (requires exactly two completed children with fully available input/output usage and the selected model/effort)")
			}
			for _, holdoutError := range holdoutErrors {
				record.Error = strings.TrimSpace(record.Error + "; " + holdoutError)
			}
			metadata.Records = append(metadata.Records, record)
			if err := checkpoint(); err != nil {
				return failWithResults(err)
			}
			fmt.Printf("%s rep=%d started=%d completed=%d child_usage=%d action_coverage=%t passed=%t\n", arm.name, rep, record.SubagentStartedChildren, record.SubagentCompletedChildren, record.SubagentChildrenWithUsage, record.SubagentActionCoverage, passed)
			if ctx.Err() != nil {
				return failWithResults(ctx.Err())
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

// Copy only implementation inputs into the model workspace. The holdout tests
// remain pristine and are overlaid later by verifyHoldout.
func copyImplementationFixture(source, destination string) error {
	if err := copyTree(source, destination); err != nil {
		return err
	}
	err := filepath.WalkDir(destination, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		return os.Remove(path)
	})
	return err
}
