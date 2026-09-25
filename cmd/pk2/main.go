// Command pk2 is the beta build of pk (mirrors OpenCode 2's `opencode2`).
//
// pk2 is a thin client over the v2 daemon (internal/v2/daemon): `pk2 daemon
// start` runs the daemon over stdio JSON-RPC; `pk2 run` is a stateless client
// that spawns the daemon (or attaches to a running one) and drives one prompt.
// It shares the stable pk's PK_HOME provider configuration read-only, and is
// intentionally a separate binary so stable pk and its running sessions are
// never disturbed.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/pkyanam/pk/internal/v2/daemon"
	"github.com/pkyanam/pk/internal/v2/jsonrpc"
)

// pk2PidFile is the pk2 daemon's private pidfile under PK_HOME (distinct from
// stable pk's pk-daemon.pid so the two daemons never collide).
const pk2PidFile = "pk2-daemon.pid"

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stdout)
		return 0
	}
	switch args[0] {
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	case "daemon":
		return runDaemon(args[1:], stdin, stdout, stderr)
	case "run":
		return runPrompt(args[1:], stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "pk2: unknown command %q (try 'pk2 --help')\n", args[0])
		return 2
	}
}

func usage(out io.Writer) {
	fmt.Fprintln(out, `Usage:
  pk2 daemon start               run the v2 daemon over stdio JSON-RPC (writes PK_HOME/pk2-daemon.pid)
  pk2 daemon stop|status         stop or check the pk2 daemon
  pk2 run [--session ID] [--model MODEL] PROMPT
                      run one agent prompt (spawns/attaches the daemon) and print the final answer

pk2 is the beta build of pk (v2 architecture, docs/v2-architecture.md). It
reuses the stable pk provider configuration under PK_HOME (read-only) and
never disturbs the stable pk binary or its sessions.`)
}

// pkHome mirrors stable pk's resolution: $PK_HOME or ~/.pk.
func pkHome() string {
	if h := strings.TrimSpace(os.Getenv("PK_HOME")); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return filepath.Join(home, ".pk")
}

// runDaemon implements pk2 daemon start|stop|status.
func runDaemon(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pk2 daemon start|stop|status")
		return 2
	}
	switch args[0] {
	case "start":
		home := pkHome()
		if err := ensurePrivateDir(home); err != nil {
			fmt.Fprintf(stderr, "pk2 daemon start: %v\n", err)
			return 1
		}
		if err := os.WriteFile(filepath.Join(home, pk2PidFile), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			fmt.Fprintf(stderr, "pk2 daemon start: write pidfile: %v\n", err)
			return 1
		}
		defer func() { _ = os.Remove(filepath.Join(home, pk2PidFile)) }()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_ = ctx // Serve blocks until EOF; stdin EOF ends the daemon.
		d := daemon.New(home)
		if err := d.Serve(ctx, stdin, stdout); err != nil {
			fmt.Fprintf(stderr, "pk2 daemon: %v\n", err)
			return 1
		}
		return 0
	case "stop":
		return stopDaemon(stdout, stderr)
	case "status":
		return daemonStatus(stdout, stderr)
	default:
		fmt.Fprintf(stderr, "pk2 daemon: unknown subcommand %q\n", args[0])
		return 2
	}
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("pk home %q is not a directory", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("pk home %q must have private permissions (0700)", path)
	}
	return nil
}

func stopDaemon(stdout, stderr io.Writer) int {
	pid, err := readPid()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stdout, "pk2 daemon: not running")
			return 0
		}
		fmt.Fprintf(stderr, "pk2 daemon stop: %v\n", err)
		return 1
	}
	if err := syscall.Kill(pid, 0); err != nil {
		_ = os.Remove(filepath.Join(pkHome(), pk2PidFile))
		fmt.Fprintln(stdout, "pk2 daemon: not running (stale pidfile removed)")
		return 0
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fmt.Fprintf(stderr, "pk2 daemon stop: signal %d: %v\n", pid, err)
		return 1
	}
	fmt.Fprintf(stdout, "pk2 daemon: stopping pid %d\n", pid)
	return 0
}

func daemonStatus(stdout, stderr io.Writer) int {
	pid, err := readPid()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stdout, "pk2 daemon: stopped")
			return 0
		}
		fmt.Fprintf(stderr, "pk2 daemon status: %v\n", err)
		return 1
	}
	if err := syscall.Kill(pid, 0); err != nil {
		_ = os.Remove(filepath.Join(pkHome(), pk2PidFile))
		fmt.Fprintln(stdout, "pk2 daemon: stopped (stale pidfile removed)")
		return 0
	}
	fmt.Fprintf(stdout, "pk2 daemon: running (pid %d)\n", pid)
	return 0
}

func readPid() (int, error) {
	b, err := os.ReadFile(filepath.Join(pkHome(), pk2PidFile))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// runPrompt is the stateless client path: spawn the daemon over a pipe (or
// reuse a running one via session/attach), send one prompt, print the final
// answer. The daemon is spawned as a child of this process and terminated
// after the turn, so a `pk2 run` never leaves a stray daemon behind.
func runPrompt(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	sessionID := ""
	model := ""
	rest := args
	for len(rest) > 0 && (rest[0] == "--session" || rest[0] == "-s" || rest[0] == "--model" || rest[0] == "-m") {
		if len(rest) < 2 {
			fmt.Fprintln(stderr, "pk2 run: flag requires a value")
			return 2
		}
		switch rest[0] {
		case "--session", "-s":
			sessionID = rest[1]
		case "--model", "-m":
			model = rest[1]
		}
		rest = rest[2:]
	}
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "usage: pk2 run [--session ID] [--model MODEL] PROMPT")
		return 2
	}
	prompt := strings.Join(rest, " ")

	// Spawn the daemon child over a pipe.
	cmd := exec.Command(os.Args[0], "daemon", "start")
	cmd.Env = append(os.Environ(), "PK_HOME="+pkHome())
	cmd.Stderr = stderr
	daemonIn, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(stderr, "pk2 run: stdin pipe: %v\n", err)
		return 1
	}
	daemonOut, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(stderr, "pk2 run: stdout pipe: %v\n", err)
		return 1
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "pk2 run: start daemon: %v\n", err)
		return 1
	}
	defer func() {
		_ = daemonIn.Close()
		_ = cmd.Wait()
	}()

	client := bufio.NewReader(daemonOut)
	call := func(id any, method string, params any) (jsonrpc.Response, error) {
		var body any
		if params != nil {
			body = map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
		} else {
			body = map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
		}
		b, err := json.Marshal(body)
		if err != nil {
			return jsonrpc.Response{}, err
		}
		if _, err := fmt.Fprintln(daemonIn, string(b)); err != nil {
			return jsonrpc.Response{}, err
		}
		line, err := client.ReadString('\n')
		if err != nil {
			return jsonrpc.Response{}, err
		}
		var resp jsonrpc.Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			return jsonrpc.Response{}, err
		}
		return resp, nil
	}

	// Ensure a session exists.
	if sessionID == "" {
		params := map[string]any{}
		if model != "" {
			params["model"] = model
		}
		resp, err := call(1, "session/create", params)
		if err != nil {
			fmt.Fprintf(stderr, "pk2 run: session/create: %v\n", err)
			return 1
		}
		if resp.Error != nil {
			fmt.Fprintf(stderr, "pk2 run: session/create: %s\n", resp.Error.Message)
			return 1
		}
		result, _ := resp.Result.(map[string]any)
		sessionID, _ = result["id"].(string)
	} else {
		resp, err := call(2, "session/attach", map[string]any{"id": sessionID})
		if err != nil {
			fmt.Fprintf(stderr, "pk2 run: session/attach: %v\n", err)
			return 1
		}
		if resp.Error != nil {
			fmt.Fprintf(stderr, "pk2 run: session/attach: %s\n", resp.Error.Message)
			return 1
		}
	}

	resp, err := call(3, "prompt", map[string]any{"session_id": sessionID, "text": prompt})
	if err != nil {
		fmt.Fprintf(stderr, "pk2 run: prompt: %v\n", err)
		return 1
	}
	if resp.Error != nil {
		fmt.Fprintf(stderr, "pk2 run: %s\n", resp.Error.Message)
		return 1
	}
	result, _ := resp.Result.(map[string]any)
	text, _ := result["text"].(string)
	if tc, ok := result["tool_calls"].([]any); ok && len(tc) > 0 {
		fmt.Fprintf(stdout, "[tools used: %d]\n", len(tc))
	}
	fmt.Fprintln(stdout, text)
	return 0
}
