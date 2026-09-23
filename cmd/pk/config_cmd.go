package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pkyanam/pk/internal/config"
)

func runConfigCommand(args []string, out, errOut io.Writer) int {
	path := filepath.Join(pkHome(), "config.json")
	if len(args) > 0 && args[0] == "budget" {
		return runContextBudgetConfigCommand(path, args[1:], out, errOut)
	}
	if len(args) == 0 || (len(args) == 1 && args[0] == "show") {
		cfg, err := config.Load(path)
		if err != nil {
			fmt.Fprintf(errOut, "pk config: %v\n", err)
			return 1
		}
		imageDriver := cfg.ImageGenDriver
		if imageDriver == "" {
			imageDriver = "off"
		}
		fmt.Fprintf(out, "model = %s\neffort = %s\ncontext_policy = %s\nimage_driver = %s\ncontext_unknown_input_budget_tokens = %d\ncontext_output_reserve_tokens = %d\ncontext_safety_margin_tokens = %d\nhistory_compaction = %t\nhistory_compaction_trigger_ratio = %.2f\nhistory_compaction_target_ratio = %.2f\nfile = %s\n", cfg.Model, cfg.Effort, cfg.ContextPolicy, imageDriver, *cfg.ContextBudget.UnknownInputBudgetTokens, *cfg.ContextBudget.OutputReserveTokens, *cfg.ContextBudget.SafetyMarginTokens, *cfg.HistoryCompaction.Enabled, *cfg.HistoryCompaction.TriggerRatio, *cfg.HistoryCompaction.TargetRatio, path)
		return 0
	}
	if len(args) != 3 || args[0] != "set" {
		fmt.Fprintln(errOut, "usage: pk config [show | set model|effort|context-policy|image-driver VALUE | budget show | budget set --provider ID --model ID [--context N] [--input N] [--output N] [--reserve N] [--margin N] [--unknown-input N]]")
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
	if args[1] == "context-policy" && !config.ValidContextPolicy(value) {
		fmt.Fprintf(errOut, "pk config: unsupported context policy %q (use full or compact)\n", value)
		return 2
	}
	if args[1] == "image-driver" && !strings.EqualFold(value, "off") && !config.ValidImageGenDriver(value) {
		fmt.Fprintf(errOut, "pk config: invalid image driver %q\n", value)
		return 2
	}
	if args[1] == "effort" {
		value = strings.ToLower(value)
	}
	if args[1] == "context-policy" {
		value = strings.ToLower(value)
	}
	switch args[1] {
	case "model":
		cfg.Model = value
	case "effort":
		cfg.Effort = value
	case "context-policy":
		cfg.ContextPolicy = value
	case "image-driver":
		if strings.EqualFold(value, "off") {
			value = ""
		}
		cfg.ImageGenDriver = value
	default:
		fmt.Fprintf(errOut, "pk config: unknown key %q (use model, effort, context-policy, or image-driver)\n", args[1])
		return 2
	}
	if err := config.Save(path, cfg); err != nil {
		fmt.Fprintf(errOut, "pk config: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Saved %s = %s\n", args[1], value)
	return 0
}

func runContextBudgetConfigCommand(path string, args []string, out, errOut io.Writer) int {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(errOut, "pk config: %v\n", err)
		return 1
	}
	if len(args) == 1 && args[0] == "show" {
		fmt.Fprintf(out, "unknown_input_budget_tokens = %d\noutput_reserve_tokens = %d\nsafety_margin_tokens = %d\nhistory_compaction_enabled = %t\nhistory_compaction_trigger_ratio = %.2f\nhistory_compaction_target_ratio = %.2f\nhistory_compaction_summary_reserve_tokens = %d\nhistory_compaction_summary_input_tokens = %d\nhistory_compaction_max_summary_tokens = %d\nhistory_compaction_max_calls = %d\n", *cfg.ContextBudget.UnknownInputBudgetTokens, *cfg.ContextBudget.OutputReserveTokens, *cfg.ContextBudget.SafetyMarginTokens, *cfg.HistoryCompaction.Enabled, *cfg.HistoryCompaction.TriggerRatio, *cfg.HistoryCompaction.TargetRatio, *cfg.HistoryCompaction.SummaryReserveTokens, *cfg.HistoryCompaction.SummaryInputTokens, *cfg.HistoryCompaction.MaxSummaryTokens, *cfg.HistoryCompaction.MaxSummaryCalls)
		for _, override := range cfg.ContextBudget.Overrides {
			fmt.Fprintf(out, "override %s/%s context=%s input=%s output=%s\n", override.ProviderID, override.ModelID, optionalTokenValue(override.ContextTokens), optionalTokenValue(override.InputTokens), optionalTokenValue(override.OutputTokens))
		}
		return 0
	}
	if len(args) == 0 || args[0] != "set" {
		fmt.Fprintln(errOut, "usage: pk config budget show | budget set --provider ID --model ID [--context N] [--input N] [--output N] [--reserve N] [--margin N] [--unknown-input N]")
		return 2
	}
	flags := flag.NewFlagSet("pk config budget set", flag.ContinueOnError)
	flags.SetOutput(errOut)
	providerID := flags.String("provider", "", "provider ID (native for the built-in Codex provider)")
	modelID := flags.String("model", "", "exact model ID")
	contextTokens := flags.String("context", "", "model context window token limit; 0 clears this override")
	inputTokens := flags.String("input", "", "model input token limit; 0 clears this override")
	outputTokens := flags.String("output", "", "model output token limit; 0 clears this override")
	reserveTokens := flags.String("reserve", "", "output token reserve; 0 is allowed")
	marginTokens := flags.String("margin", "", "safety margin token reserve; 0 is allowed")
	unknownTokens := flags.String("unknown-input", "", "operational input budget when no model limit is known; must be positive")
	compaction := flags.String("compaction", "", "history compaction on or off")
	triggerRatio := flags.String("trigger", "", "history compaction trigger ratio (0..1)")
	targetRatio := flags.String("target", "", "history compaction target ratio (0..trigger)")
	summaryReserve := flags.String("summary-reserve", "", "tokens reserved for compact summary output")
	summaryInput := flags.String("summary-input", "", "maximum source tokens provided to summary model")
	maxSummary := flags.String("max-summary", "", "maximum summary output tokens")
	maxCalls := flags.String("max-calls", "", "maximum compaction model calls")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(errOut, "pk config budget: unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}
	changed := false
	hasModelLimit := *contextTokens != "" || *inputTokens != "" || *outputTokens != ""
	if *providerID != "" || *modelID != "" || hasModelLimit {
		if strings.TrimSpace(*providerID) == "" || strings.TrimSpace(*modelID) == "" {
			fmt.Fprintln(errOut, "pk config budget: --provider and --model are required for model limits")
			return 2
		}
		if !hasModelLimit {
			fmt.Fprintln(errOut, "pk config budget: specify at least one of --context, --input, or --output")
			return 2
		}
		index := -1
		for i, override := range cfg.ContextBudget.Overrides {
			if strings.EqualFold(override.ProviderID, *providerID) && strings.EqualFold(override.ModelID, *modelID) {
				index = i
				break
			}
		}
		var override config.ContextBudgetOverride
		if index >= 0 {
			override = cfg.ContextBudget.Overrides[index]
		} else {
			override = config.ContextBudgetOverride{ProviderID: strings.TrimSpace(*providerID), ModelID: strings.TrimSpace(*modelID)}
		}
		for _, item := range []struct {
			name, value string
			target      **int64
		}{{"--context", *contextTokens, &override.ContextTokens}, {"--input", *inputTokens, &override.InputTokens}, {"--output", *outputTokens, &override.OutputTokens}} {
			if item.value == "" {
				continue
			}
			value, parseErr := strconv.ParseInt(item.value, 10, 64)
			if parseErr != nil || value < 0 {
				fmt.Fprintf(errOut, "pk config budget: %s must be 0 or a positive integer\n", item.name)
				return 2
			}
			if value == 0 {
				*item.target = nil
			} else {
				*item.target = &value
			}
		}
		if override.ContextTokens == nil && override.InputTokens == nil && override.OutputTokens == nil {
			if index >= 0 {
				cfg.ContextBudget.Overrides = append(cfg.ContextBudget.Overrides[:index], cfg.ContextBudget.Overrides[index+1:]...)
			}
		} else if index >= 0 {
			cfg.ContextBudget.Overrides[index] = override
		} else {
			cfg.ContextBudget.Overrides = append(cfg.ContextBudget.Overrides, override)
		}
		changed = true
	}
	for _, item := range []struct {
		name, value string
		target      **int64
		allowZero   bool
	}{{"--reserve", *reserveTokens, &cfg.ContextBudget.OutputReserveTokens, true}, {"--margin", *marginTokens, &cfg.ContextBudget.SafetyMarginTokens, true}, {"--unknown-input", *unknownTokens, &cfg.ContextBudget.UnknownInputBudgetTokens, false}} {
		if item.value == "" {
			continue
		}
		value, parseErr := strconv.ParseInt(item.value, 10, 64)
		if parseErr != nil || value < 0 || (!item.allowZero && value == 0) {
			fmt.Fprintf(errOut, "pk config budget: invalid %s value\n", item.name)
			return 2
		}
		*item.target = &value
		changed = true
	}
	if *compaction != "" {
		var enabled bool
		switch strings.ToLower(strings.TrimSpace(*compaction)) {
		case "on":
			enabled = true
		case "off":
			enabled = false
		default:
			fmt.Fprintln(errOut, "pk config budget: --compaction must be on or off")
			return 2
		}
		cfg.HistoryCompaction.Enabled = &enabled
		changed = true
	}
	for _, item := range []struct {
		name, value string
		target      **float64
	}{{"--trigger", *triggerRatio, &cfg.HistoryCompaction.TriggerRatio}, {"--target", *targetRatio, &cfg.HistoryCompaction.TargetRatio}} {
		if item.value == "" {
			continue
		}
		value, parseErr := strconv.ParseFloat(item.value, 64)
		if parseErr != nil {
			fmt.Fprintf(errOut, "pk config budget: invalid %s ratio\n", item.name)
			return 2
		}
		*item.target = &value
		changed = true
	}
	for _, item := range []struct {
		name, value string
		target      **int64
	}{{"--summary-reserve", *summaryReserve, &cfg.HistoryCompaction.SummaryReserveTokens}, {"--summary-input", *summaryInput, &cfg.HistoryCompaction.SummaryInputTokens}, {"--max-summary", *maxSummary, &cfg.HistoryCompaction.MaxSummaryTokens}, {"--max-calls", *maxCalls, &cfg.HistoryCompaction.MaxSummaryCalls}} {
		if item.value == "" {
			continue
		}
		value, parseErr := strconv.ParseInt(item.value, 10, 64)
		if parseErr != nil || value < 0 {
			fmt.Fprintf(errOut, "pk config budget: invalid %s value\n", item.name)
			return 2
		}
		*item.target = &value
		changed = true
	}
	if !changed {
		fmt.Fprintln(errOut, "pk config budget: specify at least one limit or reserve to change")
		return 2
	}
	if err := config.Save(path, cfg); err != nil {
		fmt.Fprintf(errOut, "pk config budget: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, "Saved context budget defaults.")
	return 0
}

func optionalTokenValue(value *int64) string {
	if value == nil {
		return "unknown"
	}
	return strconv.FormatInt(*value, 10)
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
