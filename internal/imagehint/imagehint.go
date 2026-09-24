// Package imagehint adds display guidance only after an image tool succeeds.
package imagehint

import (
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const Text = "Show images to the user with ![alt](relative/path.png) in replies; ViewImage only lets the model see them."

type translator struct{ tool.Translator }

func Wrap(base tool.Translator) tool.Translator { return translator{base} }

func (t translator) TranslateResult(id string, status tool.CallStatus, operations []operation.Operation) (llm.ToolResult, error) {
	result, err := t.Translator.TranslateResult(id, status, operations)
	if err != nil || status.Error != "" || len(operations) == 0 {
		return result, err
	}
	for _, op := range operations {
		if op.Status != operation.StatusCompleted {
			return result, err
		}
	}
	result.Output = append(result.Output, llm.ToolResultOutput{Kind: llm.ToolResultText, Value: Text})
	return result, nil
}
