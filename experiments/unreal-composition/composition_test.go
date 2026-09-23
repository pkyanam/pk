package composition_test

import (
	"reflect"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openai"
)

// This external module deliberately imports only public upstream packages.
// No provider request is sent; the key is a dummy value.
func TestPublicClientCanBeConstructed(t *testing.T) {
	client, err := openai.NewClient(openai.Config{
		APIKey:  "not-a-real-key",
		BaseURL: "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	var _ llm.Adapter = client
}

// A completed result must not rewrite history already sent to the provider.
// This checks the composition boundary and prefix behavior pk would inherit;
// it does not measure actual provider cache hits or token savings.
func TestAsyncCompletionPreservesSubmittedPrefix(t *testing.T) {
	builder := contextbuilder.NewBuilder()
	builder.AddModelResponse(llm.Response{Output: []llm.Item{{
		Type: llm.ItemToolCall,
		Data: llm.ToolCall{CallID: "slow-job", Name: "Bash", Arguments: `{"command":"sleep 1"}`},
	}}})
	builder.AddToolResult("slow-job", nil, true)
	submitted, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	builder.Commit()
	builder.AddToolResult("slow-job", []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "finished"}}, false)
	completed, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(completed.Request.Input) != len(submitted.Request.Input)+1 {
		t.Fatalf("completion should append one result: before=%d after=%d", len(submitted.Request.Input), len(completed.Request.Input))
	}
	if !reflect.DeepEqual(submitted.Request.Input, completed.Request.Input[:len(submitted.Request.Input)]) {
		t.Fatal("submitted prefix changed")
	}
	last := completed.Request.Input[len(completed.Request.Input)-1].Data.(llm.ToolResult)
	if last.CallID != "slow-job" || last.Output[0].Value != "finished" {
		t.Fatalf("unexpected completion: %+v", last)
	}
}
