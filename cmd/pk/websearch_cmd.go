package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pkyanam/pk/internal/websearch"
	"golang.org/x/term"
)

func runWebCommand(_ context.Context, args []string, input io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 {
		webUsage(errOut)
		return 2
	}
	store := websearch.SecretStore{Home: pkHome()}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			webUsage(errOut)
			return 2
		}
		status, err := webStatusPayload()
		if err != nil {
			fmt.Fprintln(errOut, "pk web status: could not inspect TinyFish configuration")
			return 1
		}
		if !status["configured"].(bool) {
			fmt.Fprintln(out, "TinyFish Search/Fetch: not configured.")
			fmt.Fprintln(out, "Run `pk web setup` for the private setup flow, or set TINYFISH_API_KEY before launching pk.")
			return 0
		}
		fmt.Fprintf(out, "TinyFish Search/Fetch: configured via %s (credential hidden; validity not checked).\n", status["source"])
		fmt.Fprintln(out, "Web tools are available to newly started sessions. Use /new to add them to a saved session.")
		return 0
	case "setup":
		if len(args) != 1 {
			webUsage(errOut)
			return 2
		}
		fmt.Fprintln(out, "Create a TinyFish API key from https://tinyfish.ai, then run `pk web configure` to enter it without echo.")
		fmt.Fprintln(out, "Keys are stored in ~/.pk/websearch.json with owner-only permissions. The TINYFISH_API_KEY environment variable takes precedence.")
		fmt.Fprintln(out, "Check setup with `pk web status`. Clear the pk-managed key with `pk web clear`.")
		return 0
	case "configure":
		if len(args) != 1 {
			webUsage(errOut)
			return 2
		}
		secret, err := readWebSecret(input, errOut)
		if err != nil {
			fmt.Fprintln(errOut, "pk web configure: could not read a key; no value was saved")
			return 1
		}
		if err := store.Save(secret); err != nil {
			fmt.Fprintf(errOut, "pk web configure: %v\n", err)
			return 1
		}
		fmt.Fprintln(out, "TinyFish key saved to the private pk credential store. Use `pk web status` to verify configuration.")
		return 0
	case "clear":
		if len(args) != 1 {
			webUsage(errOut)
			return 2
		}
		if err := store.Clear(); err != nil {
			fmt.Fprintf(errOut, "pk web clear: %v\n", err)
			return 1
		}
		if strings.TrimSpace(os.Getenv("TINYFISH_API_KEY")) != "" {
			fmt.Fprintln(out, "Removed the pk-managed TinyFish key. TINYFISH_API_KEY remains active and takes precedence.")
		} else {
			fmt.Fprintln(out, "Removed the pk-managed TinyFish key.")
		}
		return 0
	default:
		webUsage(errOut)
		return 2
	}
}

func webStatusPayload() (map[string]any, error) {
	key, source, err := websearch.ResolveAPIKey(pkHome())
	if err != nil {
		return nil, errors.New("could not inspect TinyFish configuration")
	}
	return map[string]any{
		"configured":                    key != "",
		"source":                        source,
		"tools_enabled":                 key != "",
		"tools_enabled_for_new_session": key != "",
		"credential_validity":           "not_checked",
	}, nil
}

func webConfigureSecret(secret string) (map[string]any, error) {
	if err := (websearch.SecretStore{Home: pkHome()}).Save(secret); err != nil {
		return nil, err
	}
	return webStatusPayload()
}

func webClearSecret() (map[string]any, error) {
	if err := (websearch.SecretStore{Home: pkHome()}).Clear(); err != nil {
		return nil, err
	}
	return webStatusPayload()
}

func readWebSecret(input io.Reader, prompt io.Writer) (string, error) {
	if file, ok := input.(*os.File); ok && file == os.Stdin && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(prompt, "TinyFish API key (input hidden): ")
		value, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(value)), nil
	}
	reader := bufio.NewReader(io.LimitReader(input, 8193))
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if len(value) > 8192 {
		return "", errors.New("key is too long")
	}
	return strings.TrimSpace(value), nil
}

func webUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage: pk web status | setup | configure | clear")
}
