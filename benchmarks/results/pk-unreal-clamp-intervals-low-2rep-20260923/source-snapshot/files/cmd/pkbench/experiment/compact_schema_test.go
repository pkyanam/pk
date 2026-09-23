package experiment

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestCompactToolDescriptionsOnlyChangesDescriptiveText(t *testing.T) {
	base := tool.NewRegistry(tool.StaticTranslators{}, tool.BashName, tool.ViewImageName, tool.SkillUseName)
	original := base.StaticDefinitions()
	compacted, metrics := CompactToolDescriptions(base)
	got := compacted.StaticDefinitions()

	if len(original) != len(got) {
		t.Fatalf("definitions count = %d, want %d", len(got), len(original))
	}
	for i := range original {
		before, after := original[i], got[i]
		if before.Tool.Name != after.Tool.Name || before.Tool.Type != after.Tool.Type {
			t.Fatalf("tool identity changed: before=%#v after=%#v", before.Tool, after.Tool)
		}
		if !reflect.DeepEqual(before.Metadata, after.Metadata) {
			t.Fatalf("metadata changed for %s", before.Tool.Name)
		}
		if before.Tool.Name == tool.BashName || before.Tool.Name == tool.SkillUseName {
			if after.Tool.Description == "" || len(after.Tool.Description) >= len(before.Tool.Description) {
				t.Fatalf("tool description was not reduced for %s: %q -> %q", before.Tool.Name, before.Tool.Description, after.Tool.Description)
			}
		}
		if !reflect.DeepEqual(stripDescriptions(before.Tool.Parameters), stripDescriptions(after.Tool.Parameters)) {
			t.Fatalf("non-description parameter schema changed for %s\nbefore=%#v\nafter=%#v", before.Tool.Name, before.Tool.Parameters, after.Tool.Parameters)
		}
	}
	if metrics.BeforeBytes != 573 || metrics.AfterBytes != 452 || metrics.Changed != 5 {
		t.Fatalf("schema metrics do not show compaction: %#v", metrics)
	}
	if metrics.BeforeBytes-metrics.AfterBytes <= 0 {
		t.Fatalf("no description bytes saved: %#v", metrics)
	}
}

func stripDescriptions(value any) any {
	switch current := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, child := range current {
			if key != "description" {
				result[key] = stripDescriptions(child)
			}
		}
		return result
	case []any:
		result := make([]any, len(current))
		for i, child := range current {
			result[i] = stripDescriptions(child)
		}
		return result
	default:
		return current
	}
}

func TestCompactSchemaLeavesOriginalRegistryAndTranslatorsUntouched(t *testing.T) {
	base := tool.NewRegistry(tool.StaticTranslators{}, tool.BashName, tool.ViewImageName, tool.SkillUseName)
	before, err := json.Marshal(base.StaticDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	compacted, _ := CompactToolDescriptions(base)
	_ = compacted.StaticDefinitions()
	after, err := json.Marshal(base.StaticDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("decorating registry mutated the underlying static definitions")
	}
	if _, ok := compacted.Resolve(tool.BashName); !ok {
		t.Fatal("decorated registry lost Bash translator")
	}
}

func TestCompactSchemaPreservesOutputLimitContractAndDefault(t *testing.T) {
	base := tool.NewRegistry(tool.StaticTranslators{}, tool.BashName)
	compacted, _ := CompactToolDescriptions(base)
	definitions := compacted.StaticDefinitions()
	properties := definitions[0].Tool.Parameters["properties"].(map[string]any)
	limit := properties["max_output_length"].(map[string]any)
	if limit["default"] != 40000 || limit["minimum"] != 1 || limit["maximum"] != 1000000 || limit["type"] != "integer" {
		t.Fatalf("max_output_length schema changed beyond its description: %#v", limit)
	}
}
