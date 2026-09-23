package main

import (
	"encoding/json"
	"sort"
	"strings"
)

func validSubagentActionCoverage(record runRecord) bool {
	return subagentActionCoverageAvailable(record) &&
		record.SubagentStartedChildren >= 2 && record.SubagentCompletedChildren >= 2 &&
		record.SubagentChildrenWithUsage >= 2
}

func subagentActionCoverageAvailable(record runRecord) bool {
	return record.SubagentStartedAvailable && record.SubagentStateAvailable && record.SubagentChildrenUsageAvail &&
		record.SubagentInputAvailable && record.SubagentOutputAvailable
}

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

func observeChildState(sum *usageSum, event map[string]any) {
	sum.childActivityObserved = true
	childID, state := stringValue(event["child_id"]), stringValue(event["state"])
	if childID == "" || (state != "completed" && state != "failed" && state != "canceled") {
		sum.childUnknownResponse = true
		return
	}
	if sum.childStates == nil {
		sum.childStates = map[string]string{}
	}
	sum.childStates[childID] = state
}

func observeChildModel(sum *usageSum, event map[string]any) {
	childID, model, effort := stringValue(event["child_id"]), strings.TrimSpace(stringValue(event["model"])), strings.TrimSpace(stringValue(event["effort"]))
	if childID == "" || model == "" || effort == "" || !boolValue(event["model_available"]) || !boolValue(event["effort_available"]) {
		sum.childModelInvalid = true
		return
	}
	if sum.childModels == nil {
		sum.childModels = make(map[string]subagentModelRecord)
	}
	record := subagentModelRecord{ChildID: childID, Model: model, Effort: effort}
	if previous, exists := sum.childModels[childID]; exists && previous != record {
		sum.childModelInvalid = true
		return
	}
	sum.childModels[childID] = record
}

func childModelRecords(models map[string]subagentModelRecord) []subagentModelRecord {
	result := make([]subagentModelRecord, 0, len(models))
	for _, model := range models {
		result = append(result, model)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ChildID < result[j].ChildID })
	return result
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
	sum.subagentStartedChildren = len(sum.childStarted)
	sum.subagentStartedAvailable = !sum.childUnknownResponse && (!sum.childStartToolSeen || len(sum.childStarted) > 0)
	sum.subagentStateAvailable = sum.subagentStartedAvailable
	sum.subagentChildrenUsageAvailable = sum.subagentStartedAvailable
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
			sum.subagentChildrenUsageAvailable = false
		}
		state, stateFound := sum.childStates[childID]
		if !stateFound {
			sum.subagentStateAvailable = false
		} else if state == "completed" {
			sum.subagentCompletedChildren++
		}
		if !childHasCompleteUsage(sum.childResponses, childID) {
			sum.subagentChildrenUsageAvailable = false
		} else {
			sum.subagentChildrenWithUsage++
		}
	}
	sum.subagentModelsAvailable = len(sum.childStarted) > 0 && !sum.childModelInvalid && len(sum.childModels) == len(sum.childStarted)
	if sum.subagentModelsAvailable {
		for childID := range sum.childStarted {
			if model, ok := sum.childModels[childID]; !ok || model.Model == "" || model.Effort == "" {
				sum.subagentModelsAvailable = false
				break
			}
		}
	}
	if !sum.childActivityObserved && !sum.childStartToolSeen {
		// No child tool call or child lifecycle event was present in this trace.
		sum.subagentResponsesAvailable = true
		sum.subagentStartedAvailable = true
		sum.subagentStateAvailable = true
		sum.subagentChildrenUsageAvailable = true
	}
	if sum.childUnknownResponse {
		sum.subagentStartedAvailable = false
		sum.subagentStateAvailable = false
		sum.subagentChildrenUsageAvailable = false
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
	if !responseCachesConsistent(sum.childResponses) {
		sum.subagentCachedAvailable = false
	}

	parentResponses := len(sum.parentResponses)
	parentResponseCountAvailable := !sum.parentUnknownResponse && len(sum.parentResponses) > 0
	parentUsageComplete := parentResponseCountAvailable && allResponseUsagePresent(sum.parentResponses)
	if !responseCachesConsistent(sum.parentResponses) {
		sum.cachedAvailable = false
	}
	parentInput, parentOutput, parentCached, parentWrites := sumParentValues(sum.parentResponses)
	sum.combinedResponses = parentResponses + len(sum.childResponses)
	sum.combinedResponsesAvailable = parentResponseCountAvailable && sum.subagentResponsesAvailable
	sum.combinedInput, sum.combinedOutput = parentInput+sum.subagentInput, parentOutput+sum.subagentOutput
	sum.combinedCached, sum.combinedWrites = parentCached+sum.subagentCached, parentWrites+sum.subagentWrites
	sum.combinedInputAvailable = parentUsageComplete && allResponseMetricAvailable(sum.parentResponses, "input") && sum.subagentInputAvailable
	sum.combinedOutputAvailable = parentUsageComplete && allResponseMetricAvailable(sum.parentResponses, "output") && sum.subagentOutputAvailable
	sum.combinedCachedAvailable = parentUsageComplete && allResponseMetricAvailable(sum.parentResponses, "cached") && responseCachesConsistent(sum.parentResponses) && sum.subagentCachedAvailable
	sum.combinedWritesAvailable = parentUsageComplete && allResponseMetricAvailable(sum.parentResponses, "writes") && sum.subagentWritesAvailable
}

func childHasCompleteUsage(responses map[responseKey]*responseUsage, childID string) bool {
	count := 0
	for key, response := range responses {
		if key.childID != childID {
			continue
		}
		count++
		if !response.usageSeen || !response.inputAvailable || !response.outputAvailable {
			return false
		}
	}
	return count > 0
}

func allResponseUsagePresent[K comparable](responses map[K]*responseUsage) bool {
	for _, response := range responses {
		if !response.usageSeen {
			return false
		}
	}
	return true
}

func responseCachesConsistent[K comparable](responses map[K]*responseUsage) bool {
	for _, response := range responses {
		if response.inputAvailable && response.cachedAvailable && response.cached > response.input {
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
