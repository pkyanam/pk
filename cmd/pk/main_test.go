package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseRunArgsDefaultsAndRepeatedSkillDirs(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	var stderr bytes.Buffer
	options, useCodex, codexPath, err := parseRunArgs([]string{
		"--prompt", "inspect this repo", "--workspace", t.TempDir(),
		"--skills-dir", "/tmp/one", "--skills-dir", "/tmp/two", "--jsonl",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if useCodex || codexPath != "" {
		t.Fatalf("Codex credential reuse = (%v, %q), want disabled", useCodex, codexPath)
	}
	if options.Model != "gpt-6-luna" || options.Effort != "medium" || !options.JSONL {
		t.Errorf("defaults/options = model %q, effort %q, JSONL %v", options.Model, options.Effort, options.JSONL)
	}
	if !reflect.DeepEqual(options.SkillsDirs, []string{"/tmp/one", "/tmp/two"}) {
		t.Errorf("skills dirs = %#v", options.SkillsDirs)
	}
	if !filepath.IsAbs(options.Workspace) || options.Prompt != "inspect this repo" {
		t.Errorf("workspace/prompt = %q / %q", options.Workspace, options.Prompt)
	}
}

func TestParseRunArgsRepeatedFiles(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	workspace := t.TempDir()
	options, _, _, files, err := parseRunArgsWithFiles([]string{
		"-p", "summarize", "--workspace", workspace,
		"--file", "notes.txt", "--file", "docs/report.pdf",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{"notes.txt", "docs/report.pdf"}) {
		t.Fatalf("files = %#v", files)
	}
	if options.Prompt != "summarize" {
		t.Fatalf("prompt = %q", options.Prompt)
	}
}

func TestParseRunArgsExplicitExtensionsAndResumeGuard(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	workspace := t.TempDir()
	options, _, _, _, manifests, imageDriver, err := parseRunArgsWithInputs([]string{
		"-p", "summarize", "--workspace", workspace,
		"--extension", "./one.json", "--extension", "/tmp/two.json", "--image-driver", "gpt-6-astra",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if options.Workspace != workspace || !reflect.DeepEqual(manifests, []string{"./one.json", "/tmp/two.json"}) || imageDriver != "gpt-6-astra" {
		t.Fatalf("workspace=%q manifests=%#v imageDriver=%q", options.Workspace, manifests, imageDriver)
	}
	if _, _, _, _, _, _, err := parseRunArgsWithInputs([]string{"-p", "resume", "--session", "session-id", "--extension", "./one.json"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("--session plus --extension error=%v", err)
	}
	if _, _, _, _, _, _, err := parseRunArgsWithInputs([]string{"-p", "resume", "--session", "session-id", "--image-driver", "gpt-6-astra"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("--session plus --image-driver error=%v", err)
	}
}

func TestWorkspaceAttachmentPathsHandlesAbsolutePathsInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "a.txt")
	paths, err := workspaceAttachmentPaths(workspace, []string{"relative.txt", inside})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"relative.txt", "a.txt"}) {
		t.Fatalf("resolved paths = %#v", paths)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	paths, err = workspaceAttachmentPaths(workspace, []string{outside})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Rel(workspace, outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("outside path resolved to %#v, want %#v", paths, want)
	}
}

func TestParseRunArgsCodexReuseMustBeExplicit(t *testing.T) {
	for _, args := range [][]string{
		{"-p", "hello", "--codex-auth-file", "/tmp/auth.json"},
		{"-p", "hello", "unexpected"},
		{"-p", " "},
	} {
		t.Run(args[0]+"-"+args[len(args)-1], func(t *testing.T) {
			if _, _, _, err := parseRunArgs(args, &bytes.Buffer{}); err == nil {
				t.Fatalf("parseRunArgs(%q) unexpectedly succeeded", args)
			}
		})
	}
}

func TestParseRunHelpIsNotAUsageError(t *testing.T) {
	_, _, _, err := parseRunArgs([]string{"-h"}, &bytes.Buffer{})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v, want flag.ErrHelp", err)
	}
}

func TestResumePickerAliasesAreConsumedBeforeNormalTUIArguments(t *testing.T) {
	for _, alias := range []string{"-r", "-resume", "--resume"} {
		got, ok := removeResumePickerAlias([]string{alias, "--workspace", "/tmp/work"})
		if !ok || !reflect.DeepEqual(got, []string{"--workspace", "/tmp/work"}) {
			t.Fatalf("%s parsed as (%v, %v)", alias, got, ok)
		}
	}
	if got, ok := removeResumePickerAlias([]string{"--plain"}); ok || !reflect.DeepEqual(got, []string{"--plain"}) {
		t.Fatalf("ordinary flags parsed as (%v, %v)", got, ok)
	}
}

func TestEnsurePrivateDirectoryCreatesPrivatePkHome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pk-home")
	if err := ensurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("new pk home mode = %o, want 700", info.Mode().Perm())
	}
}

func TestEnsurePrivateDirectoryDoesNotChangeExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(path); err == nil {
		t.Fatal("insecure existing directory was accepted")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("existing directory mode changed to %o", info.Mode().Perm())
	}
}
