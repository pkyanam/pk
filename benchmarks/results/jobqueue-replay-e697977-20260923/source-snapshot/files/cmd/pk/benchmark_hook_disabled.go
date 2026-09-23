//go:build !pkbench

package main

import "github.com/pkyanam/pk/internal/runner"

func beginBenchmarkRun(*runner.Options) (func(), error) { return func() {}, nil }
