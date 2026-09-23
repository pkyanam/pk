package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/pkyanam/pk/internal/mcpclient"
)

func runMCPCommand(_ context.Context, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		printMCPUsage(errOut)
		return 2
	}
	store := mcpclient.ConfigStore{Home: pkHome()}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			printMCPUsage(errOut)
			return 2
		}
		servers, err := store.Summaries()
		if err != nil {
			fmt.Fprintf(errOut, "pk mcp list: %v\n", err)
			return 1
		}
		if len(servers) == 0 {
			fmt.Fprintln(out, "No MCP servers configured.")
			return 0
		}
		for _, server := range servers {
			fmt.Fprintf(out, "%s\n  command: %s\n  args: %d configured\n", server.ID, server.Command, server.ArgumentsCount)
			if server.WorkingDirectory != "" {
				fmt.Fprintf(out, "  working directory: %s\n", server.WorkingDirectory)
			} else {
				fmt.Fprintln(out, "  working directory: user home (default)")
			}
			if len(server.EnvironmentKeys) > 0 {
				fmt.Fprintf(out, "  environment keys: %s\n", strings.Join(server.EnvironmentKeys, ", "))
			}
		}
		return 0
	case "add":
		return runMCPAdd(store, args[1:], out, errOut)
	case "remove", "rm":
		if len(args) != 2 {
			printMCPUsage(errOut)
			return 2
		}
		if err := store.Remove(args[1]); err != nil {
			fmt.Fprintf(errOut, "pk mcp remove: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "Removed MCP server %s.\n", args[1])
		return 0
	default:
		printMCPUsage(errOut)
		return 2
	}
}

func runMCPAdd(store mcpclient.ConfigStore, args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet("mcp add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "stable server id")
	command := flags.String("command", "", "absolute server executable path")
	workingDirectory := flags.String("cwd", "", "optional absolute working directory")
	var arguments, environment stringFlags
	flags.Var(&arguments, "arg", "server argument (repeatable)")
	flags.Var(&environment, "env", "explicit environment entry KEY=VALUE (repeatable)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(out, "Usage: pk mcp add --id ID --command /absolute/path [--arg ARG] [--env KEY=VALUE] [--cwd DIR]")
			return 0
		}
		fmt.Fprintf(errOut, "pk mcp add: %v\n", err)
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errOut, "pk mcp add: unexpected positional arguments")
		return 2
	}
	env := make(map[string]string, len(environment))
	for _, assignment := range environment {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok || key == "" {
			fmt.Fprintf(errOut, "pk mcp add: invalid environment entry %q (use KEY=VALUE)\n", assignment)
			return 2
		}
		if _, exists := env[key]; exists {
			fmt.Fprintf(errOut, "pk mcp add: environment key %q was supplied more than once\n", key)
			return 2
		}
		env[key] = value
	}
	server := mcpclient.ServerConfig{ID: *id, Command: *command, Args: []string(arguments), Env: env, WorkingDirectory: *workingDirectory}
	if err := store.Add(server); err != nil {
		fmt.Fprintf(errOut, "pk mcp add: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Configured MCP server %s. It will start only when a run loads the configured server list.\n", server.ID)
	return 0
}

func printMCPUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage: pk mcp list | add --id ID --command /absolute/path [--arg ARG] [--env KEY=VALUE] [--cwd DIR] | remove ID")
}

type stringFlags []string

func (s *stringFlags) String() string { return strings.Join(*s, ",") }
func (s *stringFlags) Set(value string) error {
	*s = append(*s, value)
	return nil
}
