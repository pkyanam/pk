package runner

import (
	"encoding/json/v2"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/operation"
)

func TestOperationJSONEventIncludesOnlyCompletedImageGenArtifactMetadata(t *testing.T) {
	plan := operation.RemoteJobPlan{Type: "pk.imagegen", Version: 1, Data: []byte(`{}`)}
	spec, err := operation.NewRemoteJobSpec(plan)
	if err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(operation.RemoteJobState{Plan: plan, TerminalResult: `{"path":"generated_images/a.png","width":3,"height":2,"mime":"image/png"}`})
	if err != nil {
		t.Fatal(err)
	}
	current := operation.Operation{ID: "image-op", Type: spec.Type, Version: spec.Version, Status: operation.StatusCompleted, MaxOutputLength: spec.MaxOutputLength, State: state}
	event := operationJSONEvent(current)
	if _, ok := event["generated_image"]; !ok {
		t.Fatalf("completed ImageGen operation omitted metadata: %#v", event)
	}
	current.Status = operation.StatusReady
	if _, ok := operationJSONEvent(current)["generated_image"]; ok {
		t.Fatal("running image operation exposed an artifact")
	}
	current.Status = operation.StatusCompleted
	other, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: "other.remote", Version: 1, Data: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	current.Type, current.Version, current.MaxOutputLength, current.State = other.Type, other.Version, other.MaxOutputLength, other.State
	if _, ok := operationJSONEvent(current)["generated_image"]; ok {
		t.Fatal("non-ImageGen remote job exposed an artifact")
	}
}
