package runner

import (
	"github.com/pkyanam/pk/internal/contextbudget"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func referenceHistoryCut(request llm.Request, baseCut int, usable int64, policy HistoryCompactionOptions) (int, []int) {
	if len(request.Input) < 3 {
		return baseCut, nil
	}
	lastUser := -1
	for index, item := range request.Input {
		if message, ok := item.Data.(llm.Message); ok && message.Role == llm.RoleUser {
			lastUser = index
		}
	}
	candidates := safeHistoryCuts(request.Input)
	target := int64(float64(usable) * policy.TargetRatio)
	placeholder := llm.Message{Role: llm.RoleUser, Text: strings.Repeat("x", int(min(int64(maxHistorySummaryBytes), policy.MaxSummaryTokens*3)))}
	for _, cut := range candidates {
		if cut <= baseCut || cut >= len(request.Input) {
			continue
		}
		protected := []int(nil)
		for index := 1; index < cut; index++ {
			if message, ok := request.Input[index].Data.(llm.Message); ok && message.Role == llm.RoleSystem {
				protected = append(protected, index)
			}
		}
		if lastUser >= baseCut && lastUser < cut {
			found := false
			for _, index := range protected {
				if index == lastUser {
					found = true
					break
				}
			}
			if !found {
				protected = append(protected, lastUser)
			}
		}
		input := make([]llm.Item, 0, len(request.Input)-cut+2)
		input = append(input, request.Input[0])
		input = append(input, llm.Item{Type: llm.ItemMessage, Data: placeholder})
		for _, index := range protected {
			input = append(input, request.Input[index])
		}
		input = append(input, request.Input[cut:]...)
		estimate := contextbudget.EstimateRequest(llm.Request{Model: request.Model, Input: input, Tools: request.Tools})
		if estimate.Tokens != nil && *estimate.Tokens <= target {
			return cut, protected
		}
	}
	return baseCut, nil
}

func TestHistoryCutMatchesLinearReference(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for trial := 0; trial < 120; trial++ {
		req := llm.Request{Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "instructions"}}}}
		for i := 0; i < 40; i++ {
			role := llm.RoleAssistant
			switch r.Intn(8) {
			case 0:
				role = llm.RoleUser
			case 1:
				role = llm.RoleSystem
			}
			var data any = llm.Message{Role: role, Text: strings.Repeat("text", r.Intn(150))}
			// An unavailable estimate can become available once an unsupported item is removed.
			if r.Intn(25) == 0 {
				data = struct{ Unknown string }{"unknown"}
			}
			req.Input = append(req.Input, llm.Item{Type: llm.ItemMessage, Data: data})
		}
		policy := DefaultHistoryCompactionOptions()
		policy.MaxSummaryTokens = 20
		base := r.Intn(50)
		for budget := int64(100); budget < 10000; budget += 733 {
			want, wp := referenceHistoryCut(req, base, budget, policy)
			got, gp := chooseHistoryCut(req, base, budget, policy)
			if got != want || !reflect.DeepEqual(gp, wp) {
				t.Fatalf("trial %d base %d budget %d: got %d %v want %d %v", trial, base, budget, got, gp, want, wp)
			}
		}
	}
}
