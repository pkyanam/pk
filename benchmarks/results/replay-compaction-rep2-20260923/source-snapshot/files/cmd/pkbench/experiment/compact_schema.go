package experiment

import "github.com/unreallabsai/unreal-agent/harness/tool"

// ToolSchemaMetrics records the exact UTF-8 bytes occupied by descriptions in
// the static tool schema before and after the benchmark-only compaction.
type ToolSchemaMetrics struct {
	BeforeBytes int64 `json:"tool_description_bytes_before"`
	AfterBytes  int64 `json:"tool_description_bytes_after"`
	Changed     int64 `json:"tool_description_fields_changed"`
}

var compactDescriptions = map[string]string{
	"Bash":     "Run shell commands in the background; independent calls may run in parallel. Shell exit kills child processes.",
	"SkillUse": "Load a registered skill's instructions.",
}

var compactPropertyDescriptions = map[string]map[string]string{
	"Bash": {
		"max_output_length": "Character limit per output text field (default 40000). Truncated output shows its beginning and end, omitted-character count, and path to the full output.",
	},
	"ViewImage": {
		"path": "Image path, absolute or workspace-relative.",
	},
	"SkillUse": {
		"name": "Exact registered skill name.",
	},
}

// CompactToolDescriptions wraps a registry for a benchmark-only policy that
// shortens descriptive prose while preserving tool names, schemas, defaults,
// and translators. The runner's saved context captures these definitions, so
// the schema remains identical across a resumed session.
func CompactToolDescriptions(base tool.Registry) (tool.Registry, ToolSchemaMetrics) {
	original := base.StaticDefinitions()
	before := descriptionBytes(original)
	definitions := compactDefinitions(original)
	return compactRegistry{Registry: base}, ToolSchemaMetrics{
		BeforeBytes: before,
		AfterBytes:  descriptionBytes(definitions),
		Changed:     changedDescriptionFields(original, definitions),
	}
}

func MeasureToolDescriptions(base tool.Registry) ToolSchemaMetrics {
	bytes := descriptionBytes(base.StaticDefinitions())
	return ToolSchemaMetrics{BeforeBytes: bytes, AfterBytes: bytes}
}

type compactRegistry struct{ tool.Registry }

func (r compactRegistry) StaticDefinitions() []tool.Definition {
	return compactDefinitions(r.Registry.StaticDefinitions())
}

func compactDefinitions(definitions []tool.Definition) []tool.Definition {
	result := append([]tool.Definition(nil), definitions...)
	for i := range result {
		name := result[i].Tool.Name
		if replacement, ok := compactDescriptions[name]; ok {
			result[i].Tool.Description = replacement
		}
		propertyReplacements := compactPropertyDescriptions[name]
		if len(propertyReplacements) == 0 {
			continue
		}
		parameters := cloneAnyMap(result[i].Tool.Parameters)
		properties, ok := parameters["properties"].(map[string]any)
		if !ok {
			continue
		}
		properties = cloneAnyMap(properties)
		parameters["properties"] = properties
		result[i].Tool.Parameters = parameters
		for property, description := range propertyReplacements {
			schema, ok := properties[property].(map[string]any)
			if !ok {
				continue
			}
			schema = cloneAnyMap(schema)
			schema["description"] = description
			properties[property] = schema
		}
	}
	return result
}

func cloneAnyMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func descriptionBytes(definitions []tool.Definition) int64 {
	var total int64
	for _, definition := range definitions {
		total += int64(len(definition.Tool.Description))
		total += descriptionBytesInValue(definition.Tool.Parameters)
	}
	return total
}

func descriptionBytesInValue(value any) int64 {
	switch current := value.(type) {
	case map[string]any:
		var total int64
		for key, child := range current {
			if key == "description" {
				if description, ok := child.(string); ok {
					total += int64(len(description))
				}
			}
			total += descriptionBytesInValue(child)
		}
		return total
	case []any:
		var total int64
		for _, child := range current {
			total += descriptionBytesInValue(child)
		}
		return total
	default:
		return 0
	}
}

func changedDescriptionFields(before, after []tool.Definition) int64 {
	var changed int64
	for i := 0; i < len(before) && i < len(after); i++ {
		if before[i].Tool.Name != after[i].Tool.Name {
			continue
		}
		if before[i].Tool.Description != after[i].Tool.Description {
			changed++
		}
		for property := range compactPropertyDescriptions[before[i].Tool.Name] {
			oldValue := propertyDescription(before[i], property)
			newValue := propertyDescription(after[i], property)
			if oldValue != newValue {
				changed++
			}
		}
	}
	return changed
}

func propertyDescription(definition tool.Definition, property string) string {
	properties, _ := definition.Tool.Parameters["properties"].(map[string]any)
	schema, _ := properties[property].(map[string]any)
	description, _ := schema["description"].(string)
	return description
}
