#!/usr/bin/env python3
"""Summarize sanitized pk/Unreal benchmark JSONL without contacting a model."""

import argparse
import collections
import glob
import json
import os
import statistics


def read_trace(path, engine):
    events = []
    with open(path, encoding="utf-8") as stream:
        for line_number, line in enumerate(stream, 1):
            try:
                events.append(json.loads(line))
            except json.JSONDecodeError as exc:
                raise SystemExit(f"{path}:{line_number}: invalid JSON: {exc}")
    if engine == "pk":
        responses = [event for event in events if event.get("type") == "usage"]
        def usage(event, index):
            if event.get("usage_available") is not True:
                raise SystemExit(f"{path}: response {index} has unavailable usage")
            if event.get("cached_input_tokens_available") is not True:
                raise SystemExit(f"{path}: response {index} has unavailable cached-input usage")
            return required_counters(path, index, event, "input_tokens", "output_tokens", "cached_input_tokens")
        tool_ids = {
            event.get("call_id")
            for event in events
            if event.get("type") == "tool_call" and event.get("call_id")
        }
        markers = collections.Counter(event.get("type") for event in events)
        metrics = [event for event in events if event.get("type") == "benchmark_context"]
        return responses, usage, len(tool_ids), markers, metrics

    responses = [event for event in events if event.get("kind") == "model_response"]
    def usage(event, index):
        return required_counters(path, index, event.get("usage") or {}, "inputTokens", "outputTokens", "cachedInputTokens")
    markers = collections.Counter(event.get("kind") for event in events)
    # The exported Unreal trace strips tool call IDs/names, so status rows
    # cannot be deduplicated into actual calls.
    return responses, usage, None, markers, []


def required_counters(path, index, data, input_key, output_key, cached_key):
    values = []
    for key in (input_key, output_key, cached_key):
        value = data.get(key)
        if type(value) is not int or value < 0:
            raise SystemExit(f"{path}: response {index} has missing/invalid {key}")
        values.append(value)
    return tuple(values)


def summarize(directory, engine, pattern):
    files = sorted(path for path in glob.glob(os.path.join(directory, pattern))
                   if not path.endswith(".context.jsonl"))
    if not files:
        raise SystemExit(f"no {engine} traces matching {pattern!r} in {directory}")
    total = collections.Counter()
    ordinal = collections.defaultdict(list)
    paired_delta = collections.defaultdict(list)
    tool_calls = 0
    tool_status_events = 0
    markers = collections.Counter()
    phase_traces = collections.Counter()
    component_bytes = collections.defaultdict(list)
    measured_requests = 0

    for path in files:
        responses, usage_of, calls, trace_markers, metrics = read_trace(path, engine)
        for metric in metrics:
            measured_requests += 1
            for component in ("system_prompt", "tool_schemas", "tool_calls", "tool_results", "other_input"):
                size = metric.get(component, {})
                value = size.get("bytes")
                if type(value) is not int or value < 0:
                    raise SystemExit(f"{path}: invalid {component} byte count")
                component_bytes[component].append(value)
            for role, size in metric.get("message_roles", {}).items():
                if role not in ("system", "developer", "user", "assistant", "tool", "unknown"):
                    raise SystemExit(f"{path}: unknown message role in metrics")
                value = size.get("bytes")
                if type(value) is not int or value < 0:
                    raise SystemExit(f"{path}: invalid message-role byte count")
                component_bytes[f"message_role.{role}"].append(value)
        phase = "verification" if "verification" in os.path.basename(path) else "implementation"
        phase_traces[phase] += 1
        markers.update(trace_markers)
        if calls is not None:
            tool_calls += calls
        tool_status_events += trace_markers["tool_call_status"]
        trace_inputs = []
        for index, event in enumerate(responses, 1):
            input_tokens, output_tokens, cached_tokens = usage_of(event, index)
            trace_inputs.append(input_tokens)
            total.update(input=input_tokens, output=output_tokens, cached=cached_tokens)
            ordinal[(phase, index)].append((input_tokens, output_tokens, cached_tokens))
        if trace_inputs:
            for index, input_tokens in enumerate(trace_inputs[1:], 2):
                paired_delta[(phase, index)].append(input_tokens - trace_inputs[0])

    print(f"{engine}: {len(files)} traces, {sum(len(values) for values in ordinal.values())} responses")
    print(f"  input={total['input']} cached={total['cached']} uncached={total['input'] - total['cached']} output={total['output']}")
    if engine == "pk":
        print(f"  distinct tool call IDs={tool_calls}; assistant records={markers['assistant']}")
    else:
        print(f"  tool status events={tool_status_events} (IDs/names omitted); input markers={markers['input']}; turn markers={markers['turn']}")
    print("  phase-local request ordinal: n, mean input, mean output, summed cached input")
    for (phase, index), values in sorted(ordinal.items()):
        inputs = [value[0] for value in values]
        outputs = [value[1] for value in values]
        cached = sum(value[2] for value in values)
        print(f"    {phase} #{index}: n={len(values)}, input_mean={statistics.mean(inputs):.1f}, output_mean={statistics.mean(outputs):.1f}, cached_sum={cached}")
    print("  paired input growth from that trace's response #1")
    for (phase, index), values in sorted(paired_delta.items()):
        print(f"    {phase} #{index}: n={len(values)}, mean_delta={statistics.mean(values):+.1f}")
    print(f"  traces by phase: {dict(sorted(phase_traces.items()))}")
    if measured_requests:
        print(f"  component metadata: {measured_requests} recorded requests (JSON-value bytes, not tokens)")
        print("  system_prompt overlaps message_role.system; do not add them together")
        for component, values in sorted(component_bytes.items()):
            print(f"    {component}: n={len(values)}, bytes_sum={sum(values)}, bytes_mean={statistics.mean(values):.1f}")
    else:
        print("  component metadata: unavailable")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", help="directory containing sanitized per-phase JSONL traces")
    parser.add_argument("--engine", choices=("both", "pk", "unreal"), default="both",
                        help="summarize a single engine or require both (default)")
    args = parser.parse_args()
    if args.engine in ("both", "pk"):
        summarize(args.directory, "pk", "pk-*.jsonl")
    if args.engine in ("both", "unreal"):
        summarize(args.directory, "unreal", "unreal-*.jsonl")


if __name__ == "__main__":
    main()
