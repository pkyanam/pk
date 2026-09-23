package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/pkyanam/pk/internal/skillinstall"
)

func runSkillsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printSkillsUsage(stderr)
		return 2
	}
	manager := skillInstallManager()
	switch args[0] {
	case "search":
		if len(args) != 2 {
			printSkillsUsage(stderr)
			return 2
		}
		results, err := manager.Search(ctx, args[1])
		if err != nil {
			fmt.Fprintf(stderr, "pk skills search: %v\n", err)
			return 1
		}
		for _, result := range results {
			fmt.Fprintf(stdout, "%s\t%s\t%d installs\t%s\n", result.ID, result.Name, result.Installs, result.URL)
		}
		return 0
	case "list":
		if len(args) != 1 {
			printSkillsUsage(stderr)
			return 2
		}
		installed, err := manager.List()
		if err != nil {
			fmt.Fprintf(stderr, "pk skills list: %v\n", err)
			return 1
		}
		for _, item := range installed {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", item.Name, item.Source, item.Path)
		}
		return 0
	case "add":
		if len(args) < 2 || len(args) > 3 {
			printSkillsUsage(stderr)
			return 2
		}
		candidates, err := manager.Discover(ctx, args[1])
		if err != nil {
			fmt.Fprintf(stderr, "pk skills add: %v\n", err)
			return 1
		}
		selected, err := selectSkillCandidate(candidates, args[2:])
		if err != nil {
			fmt.Fprintf(stderr, "pk skills add: %v\n", err)
			return 2
		}
		installed, err := manager.Install(ctx, args[1], selected.Path)
		if err != nil {
			fmt.Fprintf(stderr, "pk skills add: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Installed %s from %s at %s. Start a new session to load it.\n", installed.Name, installed.Source, installed.Path)
		return 0
	case "remove":
		if len(args) != 2 {
			printSkillsUsage(stderr)
			return 2
		}
		if err := manager.Remove(args[1]); err != nil {
			fmt.Fprintf(stderr, "pk skills remove: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Removed managed skill %s. Start a new session to apply the change.\n", args[1])
		return 0
	default:
		printSkillsUsage(stderr)
		return 2
	}
}

func selectSkillCandidate(candidates []skillinstall.Candidate, selector []string) (skillinstall.Candidate, error) {
	if len(selector) == 0 {
		if len(candidates) == 1 {
			return candidates[0], nil
		}
		if len(candidates) == 0 {
			return skillinstall.Candidate{}, fmt.Errorf("no skills found; check the source URL")
		}
		var names []string
		for _, candidate := range candidates {
			names = append(names, candidate.Name+" ("+candidate.Path+")")
		}
		return skillinstall.Candidate{}, fmt.Errorf("source contains multiple skills; choose one: %s", strings.Join(names, ", "))
	}
	name := strings.TrimSpace(selector[0])
	for _, candidate := range candidates {
		if candidate.Name == name || candidate.Path == name || strings.TrimSuffix(candidate.Path, "/") == name {
			return candidate, nil
		}
	}
	return skillinstall.Candidate{}, fmt.Errorf("skill %q is not one of the discovered candidates", name)
}

func printSkillsUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage: pk skills search QUERY | list | add SOURCE [SKILL] | remove NAME")
}
