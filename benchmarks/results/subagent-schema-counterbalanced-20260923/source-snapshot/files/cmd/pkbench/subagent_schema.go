package main

import (
	"context"
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

func subagentSchemaArms(repetition int) []struct{ name, mode string } {
	arms := []struct{ name, mode string }{{"pk-subagent-current", "subagent-schema-current"}, {"pk-subagent-compact", "compact-subagent-schema"}}
	if repetition%2 == 0 {
		arms[0], arms[1] = arms[1], arms[0]
	}
	return arms
}

func runSubagentSchemaExperiment(repo, out string, repetitions int, phaseTimeout time.Duration, model, modelEffort string) int {
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
	wholeTimeout := 15 * time.Minute
	if repetitions > 1 {
		wholeTimeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(processContext, wholeTimeout)
	defer cancel()
	if _, err := auth.Credentials(ctx); err != nil {
		return fail(fmt.Errorf("pk credentials are unavailable: %w", err))
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
	source, err := writeSourceManifest(repoPath, resultDir)
	if err != nil {
		return fail(err)
	}
	metadata := suite{StartedAt: started, Model: model, Effort: modelEffort, Repetitions: repetitions, Timeout: phaseTimeout.String(), WholeTimeout: wholeTimeout.String(), GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, PKRevision: gitRevision(ctx, repoPath), Unreal: "not used (paired pk-only subagent schema ablation)", Experiment: "Subagent tool schema: five direct tools vs action dispatcher", SubagentSchemaExperiment: true, SourceTreeSHA256: source.TreeSHA256, GitDiffSHA256: source.DiffSHA256, TaskSelection: []string{"clamp", "intervals"}}
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
			for _, name := range []string{"clamp", "intervals"} {
				fixture := filepath.Join(repoPath, "benchmarks", "tasks", name)
				if err := copyImplementationFixture(fixture, filepath.Join(workspace, name)); err != nil {
					return failWithResults(err)
				}
			}
			if err := initializeFixtureGit(ctx, workspace); err != nil {
				return failWithResults(err)
			}
			prompt := subagentSchemaPrompt + fmt.Sprintf(" Use model %q and reasoning effort %q for both children.", model, modelEffort)
			record, runErr := runPhase(ctx, phaseOptions{engine: arm.name, phase: "delegation", prompt: prompt, effort: modelEffort, workspace: workspace, timeout: phaseTimeout, binary: pkBinary, mode: arm.mode, pkHome: pkHome, outputDir: resultDir, skillsDir: emptySkills, taskName: "clamp+intervals", repetition: rep})
			if runErr != nil {
				record.Error = sanitizeError(runErr.Error())
			}
			passedClamp, clampErr := verifyHoldout(ctx, filepath.Join(repoPath, "benchmarks", "tasks", "clamp"), filepath.Join(workspace, "clamp"), resultDir, arm.name, rep, "clamp")
			passedIntervals, intervalsErr := verifyHoldout(ctx, filepath.Join(repoPath, "benchmarks", "tasks", "intervals"), filepath.Join(workspace, "intervals"), resultDir, arm.name, rep, "intervals")
			passed := passedClamp && passedIntervals && clampErr == nil && intervalsErr == nil
			record.CorrectnessPassed = &passed
			if !record.SubagentActionCoverageAvailable || !record.SubagentActionCoverage {
				record.Error = strings.TrimSpace(record.Error + "; subagent action coverage not met (requires at least two completed children and complete input/output usage)")
			}
			if clampErr != nil {
				record.Error = strings.TrimSpace(record.Error + "; clamp holdout: " + sanitizeError(clampErr.Error()))
			}
			if intervalsErr != nil {
				record.Error = strings.TrimSpace(record.Error + "; intervals holdout: " + sanitizeError(intervalsErr.Error()))
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
