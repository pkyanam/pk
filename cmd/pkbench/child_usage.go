package main

import "encoding/json"

func observeParentResponse(sum *usageSum, event map[string]any, usage bool) {
	responseID := stringValue(event["response_id"])
	if responseID == "" {
		sum.parentUnknownResponse = true
		return
	}
	if sum.parentResponses == nil {
		sum.parentResponses = map[string]*responseUsage{}
	}
	response := sum.parentResponses[responseID]
	if response == nil {
		response = &responseUsage{}
		sum.parentResponses[responseID] = response
	}
	if usage && !response.usageSeen {
		captureResponseUsage(response, event)
	}
}

func observeChildStart(sum *usageSum, event map[string]any) {
	childID := stringValue(event["child_id"])
	sum.childActivityObserved = true
	if childID == "" {
		sum.childUnknownResponse = true
		return
	}
	if sum.childStarted == nil {
		sum.childStarted = map[string]bool{}
	}
	sum.childStarted[childID] = true
}

func observeChildResponse(sum *usageSum, event map[string]any, usage bool) {
	sum.childActivityObserved = true
	key := responseKey{childID: stringValue(event["child_id"]), responseID: stringValue(event["response_id"])}
	if key.childID == "" || key.responseID == "" {
		sum.childUnknownResponse = true
		return
	}
	if sum.childResponses == nil {
		sum.childResponses = map[responseKey]*responseUsage{}
	}
	response := sum.childResponses[key]
	if response == nil {
		response = &responseUsage{}
		sum.childResponses[key] = response
	}
	if usage && !response.usageSeen {
		captureResponseUsage(response, event)
	}
}

func observeChildAccountingUnavailable(sum *usageSum, event map[string]any) {
	sum.childActivityObserved = true
	childID, responseID := stringValue(event["child_id"]), stringValue(event["response_id"])
	if childID == "" || responseID == "" {
		sum.childUnknownResponse = true
		return
	}
	if sum.childResponses == nil {
		sum.childResponses = map[responseKey]*responseUsage{}
	}
	key := responseKey{childID: childID, responseID: responseID}
	// An explicit invalid marker makes this response's numbers untrustworthy,
	// even if an earlier duplicate looked complete.
	sum.childResponses[key] = &responseUsage{usageSeen: true}
}

func captureResponseUsage(response *responseUsage, event map[string]any) {
	response.usageSeen = true
	if boolValue(event["usage_available"]) {
		if value, ok := nonnegativeCounter(event["input_tokens"]); ok {
			response.input, response.inputAvailable = value, true
		}
		if value, ok := nonnegativeCounter(event["output_tokens"]); ok {
			response.output, response.outputAvailable = value, true
		}
	}
	if boolValue(event["cached_input_tokens_available"]) {
		if value, ok := nonnegativeCounter(event["cached_input_tokens"]); ok {
			response.cached, response.cachedAvailable = value, true
		}
	}
	if boolValue(event["cache_write_input_tokens_available"]) {
		if value, ok := nonnegativeCounter(event["cache_write_input_tokens"]); ok {
			response.writes, response.writesAvailable = value, true
		}
	}
}

func nonnegativeCounter(value any) (int64, bool) {
	switch current := value.(type) {
	case float64:
		if current < 0 || current != float64(int64(current)) || current > float64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(current), true
	case json.Number:
		result, err := current.Int64()
		return result, err == nil && result >= 0
	case int64:
		return current, current >= 0
	case int:
		return int64(current), current >= 0
	default:
		return 0, false
	}
}

func finalizeUsageAccounting(sum *usageSum) {
	sum.subagentResponsesAvailable = !sum.childUnknownResponse && (!sum.childStartToolSeen || len(sum.childStarted) > 0)
	for childID := range sum.childStarted {
		found := false
		for key := range sum.childResponses {
			if key.childID == childID {
				found = true
				break
			}
		}
		if !found {
			sum.subagentResponsesAvailable = false
		}
	}
	if !sum.childActivityObserved && !sum.childStartToolSeen {
		// No child tool call or child lifecycle event was present in this trace.
		sum.subagentResponsesAvailable = true
	}
	childUsageComplete := sum.subagentResponsesAvailable && allResponseUsagePresent(sum.childResponses)
	sum.subagentInputAvailable, sum.subagentOutputAvailable = childUsageComplete, childUsageComplete
	sum.subagentCachedAvailable, sum.subagentWritesAvailable = childUsageComplete, childUsageComplete
	for _, response := range sum.childResponses {
		sum.subagentInput += response.input
		sum.subagentOutput += response.output
		sum.subagentCached += response.cached
		sum.subagentWrites += response.writes
		sum.subagentInputAvailable = sum.subagentInputAvailable && response.inputAvailable
		sum.subagentOutputAvailable = sum.subagentOutputAvailable && response.outputAvailable
		sum.subagentCachedAvailable = sum.subagentCachedAvailable && response.cachedAvailable
		sum.subagentWritesAvailable = sum.subagentWritesAvailable && response.writesAvailable
	}

	parentResponses := len(sum.parentResponses)
	parentResponseCountAvailable := !sum.parentUnknownResponse && len(sum.parentResponses) > 0
	parentUsageComplete := parentResponseCountAvailable && allResponseUsagePresent(sum.parentResponses)
	parentInput, parentOutput, parentCached, parentWrites := sumParentValues(sum.parentResponses)
	sum.combinedResponses = parentResponses + len(sum.childResponses)
	sum.combinedResponsesAvailable = parentResponseCountAvailable && sum.subagentResponsesAvailable
	sum.combinedInput, sum.combinedOutput = parentInput+sum.subagentInput, parentOutput+sum.subagentOutput
	sum.combinedCached, sum.combinedWrites = parentCached+sum.subagentCached, parentWrites+sum.subagentWrites
	sum.combinedInputAvailable = parentUsageComplete && allResponseMetricAvailable(sum.parentResponses, "input") && sum.subagentInputAvailable
	sum.combinedOutputAvailable = parentUsageComplete && allResponseMetricAvailable(sum.parentResponses, "output") && sum.subagentOutputAvailable
	sum.combinedCachedAvailable = parentUsageComplete && allResponseMetricAvailable(sum.parentResponses, "cached") && sum.subagentCachedAvailable
	sum.combinedWritesAvailable = parentUsageComplete && allResponseMetricAvailable(sum.parentResponses, "writes") && sum.subagentWritesAvailable
}

func allResponseUsagePresent[K comparable](responses map[K]*responseUsage) bool {
	for _, response := range responses {
		if !response.usageSeen {
			return false
		}
	}
	return true
}

func allResponseMetricAvailable[K comparable](responses map[K]*responseUsage, metric string) bool {
	for _, response := range responses {
		switch metric {
		case "input":
			if !response.inputAvailable {
				return false
			}
		case "output":
			if !response.outputAvailable {
				return false
			}
		case "cached":
			if !response.cachedAvailable {
				return false
			}
		case "writes":
			if !response.writesAvailable {
				return false
			}
		}
	}
	return true
}

func sumParentValues(responses map[string]*responseUsage) (input, output, cached, writes int64) {
	for _, response := range responses {
		input += response.input
		output += response.output
		cached += response.cached
		writes += response.writes
	}
	return input, output, cached, writes
}
