//go:build !windows

package main

import (
	"io"
	"syscall"
)

func restartProcess(path string, args, environment []string, _ io.Reader, _ io.Writer, _ io.Writer) (int, error) {
	return -1, syscall.Exec(path, append([]string{path}, args...), environment)
}
