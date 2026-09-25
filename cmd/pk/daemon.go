package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/pkyanam/pk/internal/v2/jsonrpc"
	"github.com/pkyanam/pk/internal/v2/state"
)

// daemonPidFile is the private pidfile the daemon writes under PK_HOME. It is
// the M1 coordination mechanism for `pk daemon status` and `pk daemon stop`;
// the Unix domain socket and lockfile come in a later milestone.
const daemonPidFile = "pk-daemon.pid"

// runDaemonCommand implements `pk daemon start|stop|status`. This is the v2
// entry point from cmd/pk; everything below lives in internal/v2 and is
// independent of v0.1 internals.
func runDaemonCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pk daemon start|stop|status")
		return 2
	}
	switch args[0] {
	case "start":
		if err := ensurePrivateDirectory(pkHome()); err != nil {
			fmt.Fprintf(stderr, "pk daemon start: %v\n", err)
			return 1
		}
		if err := writeDaemonPid(); err != nil {
			fmt.Fprintf(stderr, "pk daemon start: write pidfile: %v\n", err)
			return 1
		}
		defer func() { _ = os.Remove(daemonPidPath()) }()
		if err := daemonServe(ctx, stdin, stdout); err != nil {
			fmt.Fprintf(stderr, "pk daemon: %v\n", err)
			return 1
		}
		return 0
	case "stop":
		return stopDaemon(stdout, stderr)
	case "status":
		return daemonStatus(stdout, stderr)
	default:
		fmt.Fprintf(stderr, "pk daemon: unknown subcommand %q\n", args[0])
		return 2
	}
}

// daemonServe runs the M1 daemon: build the provider-catalog State, register
// the demo transformations, rebuild once, then serve JSON-RPC 2.0 over
// newline-delimited stdio. It returns when the client closes the stream or
// ctx is cancelled.
func daemonServe(ctx context.Context, r io.Reader, w io.Writer) error {
	catalog := newDemoCatalogState()
	if err := catalog.Rebuild(); err != nil {
		return fmt.Errorf("daemon: initial catalog rebuild: %w", err)
	}
	server := &jsonrpc.Server{Handlers: map[string]func(jsonrpc.Params) (any, error){
		"ping": func(jsonrpc.Params) (any, error) {
			return "pong", nil
		},
		"catalog/get": func(jsonrpc.Params) (any, error) {
			return catalog.Value(), nil
		},
		"catalog/reload": func(jsonrpc.Params) (any, error) {
			// Simulates the hourly models.dev refresh path: the loader
			// transform re-reads the (fixture) source, then disable and
			// halve-limits run again from the base, so limits never
			// compound across reloads.
			if err := catalog.Rebuild(); err != nil {
				return nil, err
			}
			return catalog.Value(), nil
		},
	}}
	errc := make(chan error, 1)
	go func() { errc <- server.Serve(r, w) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return nil
	}
}

// newDemoCatalogState builds the M1 provider-catalog State with the three
// example transformations, in fixed order: load the fixture source, disable
// excluded providers, halve limits. The catalog/get result is the Catalog
// map (provider ID -> models), documented here as the M1 wire form.
func newDemoCatalogState() *state.State[state.Catalog] {
	catalog := state.NewCatalog()
	catalog.Add(state.TransformCatalog(loadDemoCatalog))
	catalog.Add(state.DisableProviders([]string{"sphinctral"}))
	catalog.Add(state.HalveLimits())
	return catalog
}

// loadDemoCatalog is the M1 stub data source: a small deterministic local
// fixture standing in for a models.dev-style refresh. No network fetch in M1;
// the loader shape (func() (Catalog, error)) is the one the daemon keeps.
func loadDemoCatalog() (state.Catalog, error) {
	return state.Catalog{
		"openai": {
			{ID: "gpt-6-luna", Provider: "openai", Context: 200_000, Output: 16_384, CostIn: 1.5, CostOut: 7.5},
		},
		"anthropic": {
			{ID: "claude-sonnet-4", Provider: "anthropic", Context: 100_000, Output: 8_192, CostIn: 1.0, CostOut: 5.0},
		},
		"sphinctral": {
			{ID: "sphinctral-7b", Provider: "sphinctral", Context: 32_000, Output: 4_096, CostIn: 0.1, CostOut: 0.2},
		},
	}, nil
}

func daemonPidPath() string { return filepath.Join(pkHome(), daemonPidFile) }

func writeDaemonPid() error {
	return os.WriteFile(daemonPidPath(), []byte(strconv.Itoa(os.Getpid())), 0o600)
}

func readDaemonPid() (int, error) {
	b, err := os.ReadFile(daemonPidPath())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("invalid pidfile %q: %w", daemonPidPath(), err)
	}
	return pid, nil
}

// stopDaemon reads the pidfile and sends SIGTERM to the daemon process.
func stopDaemon(stdout, stderr io.Writer) int {
	pid, err := readDaemonPid()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stdout, "pk daemon: not running (no pidfile)")
			return 0
		}
		fmt.Fprintf(stderr, "pk daemon stop: %v\n", err)
		return 1
	}
	// Signal 0 probes existence without killing.
	if err := syscall.Kill(pid, 0); err != nil {
		_ = os.Remove(daemonPidPath())
		fmt.Fprintln(stdout, "pk daemon: not running (stale pidfile removed)")
		return 0
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fmt.Fprintf(stderr, "pk daemon stop: signal %d: %v\n", pid, err)
		return 1
	}
	fmt.Fprintf(stdout, "pk daemon: stopping pid %d\n", pid)
	return 0
}

// daemonStatus reports whether the pidfile's process is alive.
func daemonStatus(stdout, stderr io.Writer) int {
	pid, err := readDaemonPid()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stdout, "pk daemon: stopped")
			return 0
		}
		fmt.Fprintf(stderr, "pk daemon status: %v\n", err)
		return 1
	}
	if err := syscall.Kill(pid, 0); err != nil {
		_ = os.Remove(daemonPidPath())
		fmt.Fprintln(stdout, "pk daemon: stopped (stale pidfile removed)")
		return 0
	}
	fmt.Fprintf(stdout, "pk daemon: running (pid %d)\n", pid)
	return 0
}