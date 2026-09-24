//go:build windows

package filetools

import "os"

func openReadFile(path string) (*os.File, error) { return os.Open(path) }
