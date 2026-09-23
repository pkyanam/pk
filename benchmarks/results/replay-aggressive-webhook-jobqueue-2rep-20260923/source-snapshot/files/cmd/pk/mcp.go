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

func runMCPCommand(ctx context.Context, args []string, out, errOut io.Writer) int {
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
			fmt.Fprintf(out, "%s\n", server.ID)
			if server.Transport == "streamable_http" {
				fmt.Fprintf(out, "  URL: %s\n", server.URL)
				if server.AuthMode != "" && server.AuthMode != "none" {
					fmt.Fprintf(out, "  auth: %s (%s)\n", server.AuthMode, server.AuthStatus)
				}
				if len(server.CredentialEnv) > 0 {
					fmt.Fprintf(out, "  credential environment: %s\n", strings.Join(server.CredentialEnv, ", "))
				}
			} else {
				fmt.Fprintf(out, "  command: %s\n  args: %d configured\n", server.Command, server.ArgumentsCount)
			}
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
	case "login":
		if len(args) != 2 {
			printMCPUsage(errOut)
			return 2
		}
		if err := mcpclient.Login(ctx, store, args[1]); err != nil {
			fmt.Fprintf(errOut, "pk mcp login: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "Authorized MCP server %s.\n", args[1])
		return 0
	case "logout":
		if len(args) != 2 {
			printMCPUsage(errOut)
			return 2
		}
		servers, err := store.List()
		if err != nil {
			fmt.Fprintf(errOut, "pk mcp logout: %v\n", err)
			return 1
		}
		for _, server := range servers {
			if server.ID == args[1] {
				if server.Auth.Mode != "oauth" {
					fmt.Fprintf(errOut, "pk mcp logout: %s does not use OAuth\n", args[1])
					return 1
				}
				if err := store.ClearOAuthSession(server.Auth.SecretRef); err != nil {
					fmt.Fprintf(errOut, "pk mcp logout: %v\n", err)
					return 1
				}
				fmt.Fprintf(out, "Cleared OAuth session for %s.\n", args[1])
				return 0
			}
		}
		fmt.Fprintf(errOut, "pk mcp logout: server %q is not configured\n", args[1])
		return 1
	case "status":
		if len(args) > 2 {
			printMCPUsage(errOut)
			return 2
		}
		servers, err := store.Summaries()
		if err != nil {
			fmt.Fprintf(errOut, "pk mcp status: %v\n", err)
			return 1
		}
		found := false
		for _, server := range servers {
			if len(args) == 1 || server.ID == args[1] {
				fmt.Fprintf(out, "%s: %s", server.ID, server.AuthStatus)
				if server.AuthMode == "oauth" && server.AuthStatus == "needs_login" {
					fmt.Fprintf(out, " (run pk mcp login %s)", server.ID)
				}
				fmt.Fprintln(out)
				found = true
			}
		}
		if !found {
			fmt.Fprintln(out, "No matching MCP server is configured.")
		}
		return 0
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
	endpoint := flags.String("url", "", "remote Streamable HTTP MCP endpoint (HTTPS, or loopback HTTP)")
	authMode := flags.String("auth", "", "remote auth: bearer-env or header-env")
	bearerEnv := flags.String("bearer-env", "", "environment variable containing bearer token")
	headerName := flags.String("header", "", "custom HTTP header name")
	headerValueEnv := flags.String("header-env", "", "environment variable containing custom header value")
	workingDirectory := flags.String("cwd", "", "optional absolute working directory")
	var arguments, environment stringFlags
	flags.Var(&arguments, "arg", "server argument (repeatable)")
	flags.Var(&environment, "env", "explicit environment entry KEY=VALUE (repeatable)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(out, "Usage: pk mcp add --id ID (--command /absolute/path [--arg ARG] [--env KEY=VALUE] [--cwd DIR] | --url https://host/mcp [--auth oauth | bearer-env --bearer-env ENV | header-env --header NAME --header-env ENV])")
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
	mode := strings.ReplaceAll(*authMode, "-", "_")
	server := mcpclient.ServerConfig{ID: *id, URL: *endpoint, Command: *command, Args: []string(arguments), Env: env, WorkingDirectory: *workingDirectory, Auth: mcpclient.HTTPAuthConfig{Mode: mode, BearerEnv: *bearerEnv, HeaderName: *headerName, HeaderValueEnv: *headerValueEnv}}
	var addErr error
	if mode == "oauth" {
		addErr = store.AddOAuth(server)
	} else {
		addErr = store.Add(server)
	}
	if addErr != nil {
		fmt.Fprintf(errOut, "pk mcp add: %v\n", addErr)
		return 1
	}
	fmt.Fprintf(out, "Configured MCP server %s. It will start only when a run loads the configured server list; remote servers connect then.\n", server.ID)
	return 0
}

func printMCPUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage: pk mcp list | add --id ID (--command /absolute/path [--arg ARG] [--env KEY=VALUE] [--cwd DIR] | --url https://host/mcp [--auth oauth | bearer-env --bearer-env ENV | header-env --header NAME --header-env ENV]) | login ID | logout ID | status [ID] | remove ID")
}

type stringFlags []string

func (s *stringFlags) String() string { return strings.Join(*s, ",") }
func (s *stringFlags) Set(value string) error {
	*s = append(*s, value)
	return nil
}
