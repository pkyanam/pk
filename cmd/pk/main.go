package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/imagegen"
	"github.com/pkyanam/pk/internal/modelstream"
	"github.com/pkyanam/pk/internal/runner"
	pkbuiltins "github.com/pkyanam/pk/skills"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/responsesapi"
)

func main() { os.Exit(runMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func runMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "--update" || args[0] == "-update") {
		args = append([]string{"update"}, args[1:]...)
	}
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		usage(stdout)
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "acp":
		return runACPCommand(ctx, args[1:], stdin, stdout, stderr)
	case "mcp":
		return runMCPCommand(ctx, args[1:], stdout, stderr)
	case "provider":
		return runProviderCommand(ctx, args[1:], stdin, stdout, stderr)
	case "update", "rollback", "version", "__install-artifacts":
		return runUpdateCommand(ctx, args, stdout, stderr)
	case "rpc":
		return rpcMain(ctx, stdin, stdout, stderr)
	case "config":
		return runConfigCommand(args[1:], stdout, stderr)
	case "task", "tasks":
		return runTaskCommand(ctx, args[1:], stdout, stderr)
	case "__task-worker":
		return runTaskWorker(ctx, args[1:], stderr)
	case "login":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: pk login")
			return 2
		}
		if err := auth.Login(ctx, auth.LoginOptions{Output: stderr}); err != nil {
			fmt.Fprintf(stderr, "pk login: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Logged in to ChatGPT.")
		return 0
	case "logout":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: pk logout")
			return 2
		}
		if err := auth.Logout(); err != nil {
			fmt.Fprintf(stderr, "pk logout: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "Logged out of pk.")
		return 0
	case "status":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: pk status")
			return 2
		}
		status := auth.Status()
		if !status.LoggedIn {
			fmt.Fprintln(stdout, "Not logged in. Run `pk login` to connect ChatGPT.")
			return 0
		}
		if status.Expired {
			fmt.Fprintf(stdout, "ChatGPT login for account %s has expired. Run `pk login` to reconnect.\n", status.AccountID)
			return 0
		}
		fmt.Fprintf(stdout, "Logged in to ChatGPT (account %s; expires %s).\n", status.AccountID, status.ExpiresAt.Local().Format("2006-01-02 15:04 MST"))
		return 0
	case "run":
		return runOneShot(ctx, args[1:], stdout, stderr)
	default:
		if hasPromptFlag(args) {
			return runOneShot(ctx, args, stdout, stderr)
		}
		plain, args := removeFlag(args, "--plain")
		if !plain {
			return launchOpenTUI(args, stdin, stdout, stderr)
		}
		return runInteractiveCommand(ctx, args, stdin, stdout, stderr)
	}
}

func hasPromptFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-p" || arg == "--prompt" || strings.HasPrefix(arg, "-p=") || strings.HasPrefix(arg, "--prompt=") {
			return true
		}
	}
	return false
}

func runOneShot(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	options, useCodex, codexPath, files, extensionPaths, imageDriver, err := parseRunArgsWithInputs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "pk run: %v\n", err)
		return 2
	}
	if len(files) > 0 {
		var loaded []attachments.Attachment
		options.Prompt, loaded, err = loadPromptAttachments(ctx, options.Workspace, options.Prompt, files)
		if err != nil {
			fmt.Fprintf(stderr, "pk run: load attachments: %v\n", err)
			return 2
		}
		writeAttachmentSummary(stderr, loaded)
	}
	client, providerSnapshot, err := prepareCLIAdapter(ctx, &options, useCodex, codexPath)
	if err != nil {
		fmt.Fprintf(stderr, "pk run: %v\n", err)
		return 1
	}
	if closer, ok := client.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	options.Adapter = client
	var imageExtension []cliRegistryExtension
	if imageDriver != "" {
		imageConfig := imagegen.Config{Driver: imageDriver, Effort: "low", CodexHome: strings.TrimSpace(os.Getenv("CODEX_HOME"))}
		imageExtension = append(imageExtension, cliRegistryExtension{Decorate: imagegen.Decorator(imageConfig, options.Workspace), RemoteJobHandlers: imagegen.HandlerFactory(imageConfig, options.Workspace)})
	}
	imageExtension = append(imageExtension, tinyFishRegistryExtension())
	host, err := configureCLIExtensions(ctx, &options, extensionPaths, nil, stderr, imageExtension...)
	if err != nil {
		fmt.Fprintf(stderr, "pk run: load extensions: %v\n", err)
		return 1
	}
	if host != nil {
		defer func() {
			if closeErr := host.Close(); closeErr != nil {
				fmt.Fprintf(stderr, "pk: close extension workers: %v\n", closeErr)
			}
		}()
	}
	mcpHost, err := configureCLIMCP(ctx, &options, stderr)
	if err != nil {
		if host != nil {
			_ = host.Close()
		}
		fmt.Fprintf(stderr, "pk run: load MCP servers: %v\n", err)
		return 1
	}
	if mcpHost != nil {
		defer func() {
			if closeErr := mcpHost.Close(); closeErr != nil {
				fmt.Fprintf(stderr, "pk: close MCP servers: %v\n", closeErr)
			}
		}()
	}
	mcpServers, err := configuredSubagentMCPServers()
	if err != nil {
		fmt.Fprintf(stderr, "pk run: read MCP configuration for subagents: %v\n", err)
		return 1
	}
	subagentManager, err := configureSubagents(ctx, &options, subagentRuntimeConfig{
		Workspace: options.Workspace, SessionDir: options.SessionDir, SkillsDirs: options.SkillsDirs,
		Effort:          options.Effort,
		PluginManifests: extensionPaths, MCPServers: mcpServers,
		InheritPlugins: len(extensionPaths) > 0, InheritMCP: len(mcpServers) > 0,
		UseCodex: useCodex, CodexPath: codexPath, ProviderConfig: providerSnapshot, Diagnostics: stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "pk run: configure subagents: %v\n", err)
		return 1
	}
	defer subagentManager.Close()
	options.Output = stdout
	options.Diagnostics = stderr
	options.OnSession = func(id string) { fmt.Fprintf(stderr, "Session: %s\n", id) }
	finishBenchmark, err := beginBenchmarkRun(&options)
	if err != nil {
		fmt.Fprintf(stderr, "pk run: benchmark instrumentation: %v\n", err)
		return 1
	}
	defer finishBenchmark()
	_, err = runner.Run(ctx, options)
	if err != nil {
		fmt.Fprintf(stderr, "pk run: %v\n", err)
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return 1
	}
	return 0
}

func runInteractiveCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options, useCodex, codexPath, err := parseInteractiveArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "pk: %v\n", err)
		return 2
	}
	client, providerSnapshot, err := prepareCLIAdapter(ctx, &options, useCodex, codexPath)
	if err != nil {
		fmt.Fprintf(stderr, "pk: %v\n", err)
		return 1
	}
	if closer, ok := client.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	options.Adapter = client
	options.Output = stdout
	options.Diagnostics = stderr
	options.ToolEvents = true
	if _, err := configureCLIExtensions(ctx, &options, nil, nil, stderr, tinyFishRegistryExtension()); err != nil {
		fmt.Fprintf(stderr, "pk: load web tools: %v\n", err)
		return 1
	}
	mcpHost, err := configureCLIMCP(ctx, &options, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pk: load MCP servers: %v\n", err)
		return 1
	}
	if mcpHost != nil {
		defer func() {
			if closeErr := mcpHost.Close(); closeErr != nil {
				fmt.Fprintf(stderr, "pk: close MCP servers: %v\n", closeErr)
			}
		}()
	}
	mcpServers, err := configuredSubagentMCPServers()
	if err != nil {
		fmt.Fprintf(stderr, "pk: read MCP configuration for subagents: %v\n", err)
		return 1
	}
	subagentManager, err := configureSubagents(ctx, &options, subagentRuntimeConfig{
		Workspace: options.Workspace, SessionDir: options.SessionDir, SkillsDirs: options.SkillsDirs,
		Effort:     options.Effort,
		MCPServers: mcpServers, InheritMCP: len(mcpServers) > 0,
		UseCodex: useCodex, CodexPath: codexPath, ProviderConfig: providerSnapshot, Diagnostics: stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "pk: configure subagents: %v\n", err)
		return 1
	}
	defer subagentManager.Close()
	err = interactiveLoop(ctx, stdin, stderr, options, runner.Run)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		fmt.Fprintf(stderr, "pk: %v\n", err)
		return 1
	}
	return 0
}

func prepareAdapter(ctx context.Context, useCodex bool, codexPath string) (*codexAdapter, error) {
	if err := ensurePrivateDirectory(pkHome()); err != nil {
		return nil, fmt.Errorf("prepare pk home: %w", err)
	}
	var credential auth.Credential
	var err error
	if useCodex {
		credential, err = auth.CredentialsFromCodex(codexPath)
	} else {
		credential, err = auth.Credentials(ctx)
	}
	if err != nil {
		return nil, err
	}
	client, err := newCodexAdapter(credential, useCodex)
	if err != nil {
		return nil, fmt.Errorf("create ChatGPT client: %w", err)
	}
	return client, nil
}

type runPromptFunc func(context.Context, runner.Options) (runner.RunResult, error)

func interactiveLoop(ctx context.Context, input io.Reader, diagnostics io.Writer, options runner.Options, runTurn runPromptFunc) error {
	reader := bufio.NewReader(input)
	closer, _ := input.(io.Closer)
	printedSession := false
	options.OnSession = func(id string) {
		if !printedSession {
			fmt.Fprintf(diagnostics, "Session: %s\n", id)
			printedSession = true
		}
	}
	for {
		if _, err := fmt.Fprint(diagnostics, "pk> "); err != nil {
			return fmt.Errorf("write prompt: %w", err)
		}
		line, readErr := readPromptLine(ctx, reader, closer)
		if ctx.Err() != nil {
			fmt.Fprintln(diagnostics, "\n[pk] interrupted; any active session is saved.")
			return ctx.Err()
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			if ctx.Err() != nil {
				fmt.Fprintln(diagnostics, "\n[pk] interrupted; any active session is saved.")
				return ctx.Err()
			}
			return fmt.Errorf("read prompt: %w", readErr)
		}
		prompt := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		switch strings.TrimSpace(prompt) {
		case "/exit", "/quit":
			return nil
		case "/help":
			fmt.Fprintln(diagnostics, "Enter a prompt and press Return. Commands: /help, /exit, /quit. Ctrl-C cancels the active run and exits; the session can be resumed with --session.")
		default:
			if strings.TrimSpace(prompt) != "" {
				options.Prompt = prompt
				result, err := runTurn(ctx, options)
				if result.SessionID != "" {
					options.SessionID = result.SessionID
				}
				if err != nil {
					if ctx.Err() != nil || errors.Is(err, context.Canceled) {
						fmt.Fprintln(diagnostics, "[pk] interrupted; session saved.")
						return err
					}
					fmt.Fprintf(diagnostics, "[pk] %v\n", err)
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

type lineRead struct {
	line string
	err  error
}

func readPromptLine(ctx context.Context, reader *bufio.Reader, closer io.Closer) (string, error) {
	result := make(chan lineRead, 1)
	go func() {
		line, err := reader.ReadString('\n')
		result <- lineRead{line: line, err: err}
	}()
	select {
	case value := <-result:
		return value.line, value.err
	case <-ctx.Done():
		if closer != nil {
			_ = closer.Close()
		}
		return "", ctx.Err()
	}
}

func parseRunArgs(args []string, stderr io.Writer) (runner.Options, bool, string, error) {
	options, useCodex, codexPath, _, err := parseRunArgsWithFiles(args, stderr)
	return options, useCodex, codexPath, err
}

func parseRunArgsWithFiles(args []string, stderr io.Writer) (runner.Options, bool, string, []string, error) {
	options, useCodex, codexPath, files, _, _, err := parseRunArgsWithInputs(args, stderr)
	return options, useCodex, codexPath, files, err
}

func parseRunArgsWithInputs(args []string, stderr io.Writer) (runner.Options, bool, string, []string, []string, string, error) {
	return parseCommandArgsWithInputs(args, stderr, true)
}

func parseInteractiveArgs(args []string, stderr io.Writer) (runner.Options, bool, string, error) {
	return parseCommandArgs(args, stderr, false)
}

func parseCommandArgs(args []string, stderr io.Writer, requirePrompt bool) (runner.Options, bool, string, error) {
	options, useCodex, codexPath, _, err := parseCommandArgsWithFiles(args, stderr, requirePrompt)
	return options, useCodex, codexPath, err
}

func parseCommandArgsWithFiles(args []string, stderr io.Writer, requirePrompt bool) (runner.Options, bool, string, []string, error) {
	options, useCodex, codexPath, files, _, _, err := parseCommandArgsWithInputs(args, stderr, requirePrompt)
	return options, useCodex, codexPath, files, err
}

func parseCommandArgsWithInputs(args []string, stderr io.Writer, requirePrompt bool) (runner.Options, bool, string, []string, []string, string, error) {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options runner.Options
	var useCodex bool
	var codexAuth string
	var providerChoice string
	var skills stringList
	var files stringList
	var extensionPaths stringList
	var imageDriver string
	flags.StringVar(&options.Prompt, "p", "", "prompt to send")
	flags.StringVar(&options.Prompt, "prompt", "", "prompt to send")
	defaults, configErr := config.Load(filepath.Join(pkHome(), "config.json"))
	if configErr != nil {
		return runner.Options{}, false, "", nil, nil, "", fmt.Errorf("load config: %w", configErr)
	}
	flags.StringVar(&options.Model, "model", defaults.Model, "model ID (default from pk config)")
	flags.StringVar(&options.Effort, "effort", defaults.Effort, "reasoning effort: low, medium, high, xhigh, max")
	flags.StringVar(&options.Workspace, "workspace", "", "working directory for Bash and relative image paths")
	flags.StringVar(&options.SessionID, "session", "", "resume an existing session ID")
	flags.StringVar(&options.SystemPrompt, "system", "", "additional system instructions")
	flags.BoolVar(&options.JSONL, "jsonl", false, "write assistant and tool events as JSONL")
	flags.BoolVar(&useCodex, "use-codex", false, "explicitly reuse existing Codex ChatGPT credentials read-only")
	flags.StringVar(&codexAuth, "codex-auth-file", "", "Codex auth file used with --use-codex")
	flags.StringVar(&providerChoice, "provider", "", "provider ID or native (default configured provider)")
	flags.Var(&skills, "skills-dir", "directory containing <skill>/SKILL.md; may be repeated")
	if requirePrompt {
		flags.Var(&files, "file", "attach a text, PDF, or image file; may be repeated")
		flags.Var(&extensionPaths, "extension", "load an extension manifest explicitly; may be repeated")
		flags.StringVar(&imageDriver, "image-driver", "", "explicitly enable ImageGen using this separate model ID")
	}
	if err := flags.Parse(args); err != nil {
		return runner.Options{}, false, "", nil, nil, "", err
	}
	provider, err := resolveCLIProvider(providerChoice, useCodex)
	if err != nil {
		return runner.Options{}, false, "", nil, nil, "", fmt.Errorf("select model provider: %w", err)
	}
	modelSet, effortSet := false, false
	flags.Visit(func(item *flag.Flag) {
		switch item.Name {
		case "model":
			modelSet = true
		case "effort":
			effortSet = true
		}
	})
	applyProviderDefaults(&options, provider, modelSet, effortSet)
	options.Effort = strings.ToLower(strings.TrimSpace(options.Effort))
	if flags.NArg() != 0 {
		return runner.Options{}, false, "", nil, nil, "", fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if requirePrompt && strings.TrimSpace(options.Prompt) == "" {
		return runner.Options{}, false, "", nil, nil, "", errors.New("-p/--prompt is required")
	}
	if codexAuth != "" && !useCodex {
		return runner.Options{}, false, "", nil, nil, "", errors.New("--codex-auth-file requires --use-codex")
	}
	if !config.ValidEffort(options.Effort) {
		return runner.Options{}, false, "", nil, nil, "", fmt.Errorf("unsupported reasoning effort %q (use low, medium, high, xhigh, or max; none is not supported by the current adapter)", options.Effort)
	}
	if (len(extensionPaths) > 0 || imageDriver != "") && options.SessionID != "" {
		return runner.Options{}, false, "", nil, nil, "", errors.New("--extension and --image-driver cannot be combined with --session; tool schemas are fixed when a session starts")
	}
	if options.Workspace == "" {
		var err error
		options.Workspace, err = os.Getwd()
		if err != nil {
			return runner.Options{}, false, "", nil, nil, "", fmt.Errorf("get working directory: %w", err)
		}
	}
	workspace, err := filepath.Abs(options.Workspace)
	if err != nil {
		return runner.Options{}, false, "", nil, nil, "", fmt.Errorf("resolve workspace: %w", err)
	}
	options.Workspace = workspace
	options.SessionDir = filepath.Join(pkHome(), "sessions")
	if len(skills) == 0 {
		options.SkillsDirs = defaultSkillDirs()
	} else {
		options.SkillsDirs = skills
	}
	if useCodex && codexAuth == "" {
		codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if codexHome == "" {
			codexHome = filepath.Join(userHome(), ".codex")
		}
		codexAuth = filepath.Join(codexHome, "auth.json")
	}
	return options, useCodex, codexAuth, files, extensionPaths, imageDriver, nil
}

func defaultSkillDirs() []string {
	home := userHome()
	dirs := []string{filepath.Join(home, ".codex", "skills"), filepath.Join(home, ".agents", "skills")}
	if bundled, err := pkbuiltins.Materialize(pkHome()); err == nil {
		dirs = append(dirs, bundled)
	} else {
		fmt.Fprintf(os.Stderr, "pk: cannot load bundled skills: %v\n", err)
	}
	return dirs
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("value must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func pkHome() string {
	if path := strings.TrimSpace(os.Getenv("PK_HOME")); path != "" {
		return path
	}
	return filepath.Join(userHome(), ".pk")
}

func ensurePrivateDirectory(path string) error {
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
		return fmt.Errorf("pk home %q must have private permissions (0700); run chmod 700 %q", path, path)
	}
	return nil
}

func userHome() string {
	if path, err := os.UserHomeDir(); err == nil && path != "" {
		return path
	}
	return "."
}

func usage(out io.Writer) {
	fmt.Fprintln(out, `Usage:
  pk [OPTIONS]               start an interactive agent in the current directory
  pk --plain [OPTIONS]       use the line-based interactive fallback
  pk -p PROMPT [OPTIONS]     send one prompt and exit
  pk rpc                     start the JSONL frontend backend
  pk acp                     serve Agent Client Protocol v1 over stdio
  pk mcp list|add|remove     manage explicitly configured MCP servers
  pk provider list|add|use  manage model providers
  pk task create -p PROMPT   start a durable background task
  pk task list|status|attach|cancel|resume ...
  pk config [show|set model|set effort VALUE]
  pk login
  pk logout
  pk status
  pk update [--source DIR]    update from official GitHub main, or local DIR
  pk --update                 alias for pk update
  pk rollback
  pk version
  pk run -p PROMPT [OPTIONS]

Run options:
  --model MODEL              model ID (default gpt-6-luna)
  --effort EFFORT            reasoning effort (default medium)
  --workspace DIR            working directory for tools
  --session ID               resume a saved session
  --file PATH                attach a text, PDF, or image (repeatable; relative to workspace)
  --extension MANIFEST       load an extension manifest explicitly (repeatable; new sessions only)
  --image-driver MODEL       explicitly enable ImageGen using a separate model (new sessions only)
  --provider ID|native       select a configured model provider for this run
  --use-codex                reuse existing Codex credentials read-only
  --jsonl                    write assistant and tool events as JSONL

Interactive commands: /help, /exit, /quit. Ctrl-C cancels the current run and exits.`)
}

// codexAdapter checks pk-owned credentials before each model request. A
// cancellation-aware semaphore serializes adapter replacement with in-flight
// requests, so a refreshed client is never closed while another call uses it.
type codexAdapter struct {
	credential auth.Credential
	client     llm.Adapter
	useCodex   bool
	get        func(context.Context) (auth.Credential, error)
	refresh    func(context.Context) (auth.Credential, error)
	newClient  func(auth.Credential) (llm.Adapter, error)
	semaphore  chan struct{}
	closed     bool
}

var _ llm.Adapter = (*codexAdapter)(nil)

func newCodexAdapter(credential auth.Credential, useCodex bool) (*codexAdapter, error) {
	adapter := &codexAdapter{
		credential: credential,
		useCodex:   useCodex,
		semaphore:  make(chan struct{}, 1),
		newClient:  newOpenAICodexClient,
	}
	if !useCodex {
		adapter.get = auth.Credentials
		adapter.refresh = auth.RefreshCredentials
	}
	client, err := adapter.newClient(credential)
	if err != nil {
		return nil, err
	}
	adapter.client = client
	return adapter, nil
}

func newOpenAICodexClient(credential auth.Credential) (llm.Adapter, error) {
	return modelstream.NewClient(modelstream.Config{AccessToken: credential.AccessToken, AccountID: credential.AccountID})
}

func (adapter *codexAdapter) acquire(ctx context.Context) error {
	select {
	case adapter.semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (adapter *codexAdapter) release() { <-adapter.semaphore }

func (adapter *codexAdapter) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	if err := adapter.acquire(ctx); err != nil {
		return llm.Response{}, err
	}
	defer adapter.release()
	if adapter.closed {
		return llm.Response{}, errors.New("ChatGPT adapter is closed")
	}
	if !adapter.useCodex {
		credential, err := adapter.get(ctx)
		if err != nil {
			return llm.Response{}, err
		}
		if err := adapter.replaceClient(credential); err != nil {
			return llm.Response{}, err
		}
	}
	response, err := adapter.client.Respond(ctx, request, options)
	if adapter.useCodex || !isUnauthorized(err) {
		return response, err
	}
	// Another pk process may already have refreshed the rotating token since
	// this request was sent. Reuse that token before forcing another rotation.
	credential, refreshErr := adapter.get(ctx)
	if refreshErr != nil {
		return llm.Response{}, errors.Join(err, fmt.Errorf("reload pk credentials: %w", refreshErr))
	}
	if credential != adapter.credential {
		if replaceErr := adapter.replaceClient(credential); replaceErr != nil {
			return llm.Response{}, replaceErr
		}
		return adapter.client.Respond(ctx, request, options)
	}
	credential, refreshErr = adapter.refresh(ctx)
	if refreshErr != nil {
		return llm.Response{}, errors.Join(err, fmt.Errorf("refresh pk credentials: %w", refreshErr))
	}
	if credential == adapter.credential {
		return llm.Response{}, err
	}
	if replaceErr := adapter.replaceClient(credential); replaceErr != nil {
		return llm.Response{}, replaceErr
	}
	return adapter.client.Respond(ctx, request, options)
}

// replaceClient is called only while the adapter semaphore is held. The prior
// Respond has returned before the old adapter is closed.
func (adapter *codexAdapter) replaceClient(credential auth.Credential) error {
	if credential == adapter.credential {
		return nil
	}
	client, err := adapter.newClient(credential)
	if err != nil {
		return err
	}
	old := adapter.client
	adapter.client, adapter.credential = client, credential
	if closer, ok := old.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	return nil
}

func (adapter *codexAdapter) Close() error {
	adapter.semaphore <- struct{}{}
	defer adapter.release()
	if adapter.closed {
		return nil
	}
	adapter.closed = true
	if closer, ok := adapter.client.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func isUnauthorized(err error) bool {
	var apiErr *responsesapi.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == 401
}
