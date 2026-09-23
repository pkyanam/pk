//go:build windows

package main

import (
	"io"
	"os/exec"
)

func restartProcess(path string, args, environment []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command(path, args...)
	cmd.Env = environment
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}
