package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/providers"
)

func runProviderCommand(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer) int {
	return runProviderCommandWithStore(ctx, args, input, stdout, stderr, providers.Store{Home: pkHome()})
}

func runProviderCommandWithStore(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer, store providers.Store) int {
	if len(args) == 0 {
		printProviderUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			printProviderUsage(stderr)
			return 2
		}
		items, err := store.Summaries()
		if err != nil {
			fmt.Fprintf(stderr, "pk provider list: %v\n", err)
			return 1
		}
		if len(items) == 0 {
			fmt.Fprintln(stdout, "No model providers configured.")
			return 0
		}
		for _, item := range items {
			fmt.Fprintf(stdout, "%s\n  protocol: %s\n  base URL: %s\n", item.ID, item.Protocol, item.BaseURL)
			if item.IsDefault {
				fmt.Fprintln(stdout, "  default: yes")
			}
			if item.APIKeyConfigured {
				if item.APIKeyEnv != "" {
					fmt.Fprintf(stdout, "  API key: environment variable %s\n", item.APIKeyEnv)
				} else {
					fmt.Fprintln(stdout, "  API key: stored privately")
				}
			} else {
				fmt.Fprintln(stdout, "  API key: none")
			}
			if item.DefaultModel != "" {
				fmt.Fprintf(stdout, "  default model: %s\n", item.DefaultModel)
			}
			if item.DefaultEffort != "" {
				fmt.Fprintf(stdout, "  default effort: %s\n", item.DefaultEffort)
			}
		}
		return 0
	case "use":
		if len(args) != 2 {
			printProviderUsage(stderr)
			return 2
		}
		if args[1] == "native" || args[1] == "codex" {
			// Native selection is represented by an empty ID.
			if err := store.SetDefault(""); err != nil {
				fmt.Fprintf(stderr, "pk provider use: %v\n", err)
				return 1
			}
			fmt.Fprintln(stdout, "Using native Codex provider by default.")
			return 0
		}
		if err := store.SetDefault(args[1]); err != nil {
			fmt.Fprintf(stderr, "pk provider use: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Using provider %s by default.\n", args[1])
		return 0
	case "add", "set":
		return runProviderPut(ctx, args[1:], input, stdout, stderr, store)
	case "remove", "rm":
		if len(args) != 2 {
			printProviderUsage(stderr)
			return 2
		}
		if err := store.Remove(args[1]); err != nil {
			fmt.Fprintf(stderr, "pk provider remove: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Removed provider %s.\n", args[1])
		return 0
	case "models":
		if len(args) != 2 {
			printProviderUsage(stderr)
			return 2
		}
		provider, err := store.Get(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "pk provider models: %v\n", err)
			return 1
		}
		models, err := provider.Models(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "pk provider models: %v\n", err)
			return 1
		}
		for _, model := range models {
			fmt.Fprintln(stdout, model.ID)
		}
		return 0
	default:
		printProviderUsage(stderr)
		return 2
	}
}

func runProviderPut(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer, store providers.Store) int {
	flags := flag.NewFlagSet("provider add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "provider ID")
	protocol := flags.String("protocol", "", "responses or chat_completions")
	baseURL := flags.String("base-url", "", "OpenAI-compatible API root URL (bare hosts default to /v1)")
	keyEnv := flags.String("api-key-env", "", "environment variable containing the API key")
	keyStdin := flags.Bool("api-key-stdin", false, "read an API key from stdin and store it in the private provider config")
	model := flags.String("model", "", "default model ID")
	effort := flags.String("effort", "", "default reasoning effort")
	reasoning := flags.Bool("reasoning-effort", false, "send reasoning_effort when supported by the provider")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, "Usage: pk provider add --id ID --protocol responses|chat_completions --base-url URL [--api-key-env NAME | --api-key-stdin] [--model ID] [--effort low|medium|high|xhigh|max] [--reasoning-effort]")
			return 0
		}
		fmt.Fprintf(stderr, "pk provider add: %v\n", err)
		return 2
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(stderr, "pk provider add: %v\n", err)
		return 1
	}
	if flags.NArg() != 0 || (*keyStdin && *keyEnv != "") {
		fmt.Fprintln(stderr, "pk provider add: unexpected arguments or conflicting API key sources")
		return 2
	}
	provider := providers.Provider{ID: *id, Protocol: providers.Protocol(*protocol), BaseURL: *baseURL, APIKeyEnv: strings.TrimSpace(*keyEnv), DefaultModel: strings.TrimSpace(*model), DefaultEffort: strings.TrimSpace(*effort), SupportsReasoningEffort: *reasoning}
	if *keyStdin {
		key, err := io.ReadAll(io.LimitReader(input, 4097))
		if err != nil {
			fmt.Fprintf(stderr, "pk provider add: read API key: %v\n", err)
			return 1
		}
		if len(key) > 4096 {
			fmt.Fprintln(stderr, "pk provider add: API key exceeds 4 KiB")
			return 2
		}
		provider.APIKey = strings.TrimSpace(string(key))
	}
	if provider.DefaultEffort == "" {
		provider.DefaultEffort = config.DefaultEffort
	}
	if err := store.Put(provider); err != nil {
		fmt.Fprintf(stderr, "pk provider add: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "Saved provider %s privately.\n", provider.ID)
	return 0
}

func printProviderUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage: pk provider list | use ID|native | add --id ID --protocol responses|chat_completions --base-url URL [--api-key-env NAME | --api-key-stdin] [--model ID] [--effort VALUE] [--reasoning-effort] | models ID | remove ID")
}
