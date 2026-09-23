//go:build !pkbench

package main

import (
	"testing"

	"github.com/pkyanam/pk/internal/runner"
)

func TestSubagentSchemaBenchmarkPolicyIsAbsentFromProductionBuild(t *testing.T) {
	t.Setenv("PK_BENCH_POLICY", "compact-subagent-schema")
	options := runner.Options{}
	finish, err := beginBenchmarkRun(&options)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	if options.DecorateRegistry != nil {
		t.Fatal("production build installed a benchmark registry decorator")
	}
}
