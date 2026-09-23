package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func launchOpenTUI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "pk: locate executable: %v\n", err)
		return 1
	}
	entry, err := locateUIEntry(executable)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		fmt.Fprintln(stderr, "Bun is required for the OpenTUI frontend; install Bun or use --plain")
		return 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	token, err := newReloadToken()
	if err != nil {
		fmt.Fprintf(stderr, "pk: prepare reload supervisor: %v\n", err)
		return 1
	}
	cmdArgs := append([]string{entry}, args...)
	cmd := exec.CommandContext(ctx, bun, cmdArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	values := map[string]string{
		"PK_EXECUTABLE":            executable,
		"PK_RELOAD_TOKEN":          token,
		"PK_RELOAD_SUPERVISOR_PID": strconv.Itoa(os.Getpid()),
		// Bun enables anonymous crash reports by default. Keep the frontend's
		// runtime opt-out explicit regardless of the caller's environment.
		"DO_NOT_TRACK": "1",
	}
	for name, flagName := range map[string]string{"PK_MODEL": "--model", "PK_EFFORT": "--effort", "PK_WORKSPACE": "--workspace", "PK_SESSION": "--session"} {
		for i, arg := range args {
			if arg == flagName && i+1 < len(args) {
				values[name] = args[i+1]
				break
			}
			if strings.HasPrefix(arg, flagName+"=") {
				values[name] = strings.TrimPrefix(arg, flagName+"=")
				break
			}
		}
	}
	cmd.Env = setEnvironment(os.Environ(), values)
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			if exit.ExitCode() == 75 {
				handoff, err := consumeReloadHandoff(token, os.Getpid())
				if err != nil {
					fmt.Fprintf(stderr, "pk: reload handoff rejected: %v\n", err)
					return 1
				}
				return restartOpenTUI(handoff, stdin, stdout, stderr)
			}
			return exit.ExitCode()
		}
		fmt.Fprintf(stderr, "pk: start OpenTUI frontend: %v\n", err)
		return 1
	}
	return 0
}

func restartOpenTUI(handoff reloadHandoff, stdin io.Reader, stdout, stderr io.Writer) int {
	launcher, err := filepath.Abs(filepath.Join(updateBinaryDir(), "pk"))
	if err != nil {
		fmt.Fprintf(stderr, "pk: locate stable launcher: %v\n", err)
		return 1
	}
	info, err := os.Stat(launcher)
	if err != nil || !info.Mode().IsRegular() {
		fmt.Fprintf(stderr, "pk: stable launcher %q is unavailable; install pk before reloading\n", launcher)
		return 1
	}
	args := reloadLaunchArgs(handoff)
	code, err := restartProcess(launcher, args, os.Environ(), stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pk: restart with latest pk failed: %v\n", err)
		return 1
	}
	return code
}

func setEnvironment(environment []string, values map[string]string) []string {
	result := make([]string, 0, len(environment)+len(values))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, replace := values[key]; !replace {
			result = append(result, item)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
