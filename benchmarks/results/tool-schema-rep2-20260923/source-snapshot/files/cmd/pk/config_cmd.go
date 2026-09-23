package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/pkyanam/pk/internal/config"
)

func runConfigCommand(args []string, out, errOut io.Writer) int {
	path := filepath.Join(pkHome(), "config.json")
	if len(args) == 0 || (len(args) == 1 && args[0] == "show") {
		cfg, err := config.Load(path)
		if err != nil {
			fmt.Fprintf(errOut, "pk config: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "model = %s\neffort = %s\nfile = %s\n", cfg.Model, cfg.Effort, path)
		return 0
	}
	if len(args) != 3 || args[0] != "set" {
		fmt.Fprintln(errOut, "usage: pk config [show | set model|effort VALUE]")
		return 2
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(errOut, "pk config: %v\n", err)
		return 1
	}
	value := strings.TrimSpace(args[2])
	if value == "" {
		fmt.Fprintln(errOut, "pk config: value must not be empty")
		return 2
	}
	if args[1] == "effort" && !config.ValidEffort(value) {
		fmt.Fprintf(errOut, "pk config: unsupported effort %q (use low, medium, high, xhigh, or max; none is not supported by the current adapter)\n", value)
		return 2
	}
	if args[1] == "effort" {
		value = strings.ToLower(value)
	}
	switch args[1] {
	case "model":
		cfg.Model = value
	case "effort":
		cfg.Effort = value
	default:
		fmt.Fprintf(errOut, "pk config: unknown key %q (use model or effort)\n", args[1])
		return 2
	}
	if err := config.Save(path, cfg); err != nil {
		fmt.Fprintf(errOut, "pk config: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Saved %s = %s\n", args[1], value)
	return 0
}
func removeFlag(args []string, flag string) (bool, []string) {
	out := make([]string, 0, len(args))
	found := false
	for _, arg := range args {
		if arg == flag {
			found = true
			continue
		}
		out = append(out, arg)
	}
	return found, out
}
