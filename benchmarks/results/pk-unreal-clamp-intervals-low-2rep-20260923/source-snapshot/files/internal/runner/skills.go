package runner

import (
	"path/filepath"

	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// deduplicateSkillFiles drops repeated references to the same canonical skill
// file while preserving discovery order. Distinct files with the same skill
// name remain present so registry registration reports the name collision.
func deduplicateSkillFiles(skills []tool.Skill) []tool.Skill {
	seen := make(map[string]struct{}, len(skills))
	unique := make([]tool.Skill, 0, len(skills))
	for _, skill := range skills {
		canonical, err := filepath.EvalSymlinks(skill.Path)
		if err == nil {
			if absolute, absErr := filepath.Abs(canonical); absErr == nil {
				canonical = absolute
			}
			canonical = filepath.Clean(canonical)
			if _, exists := seen[canonical]; exists {
				continue
			}
			seen[canonical] = struct{}{}
		}
		unique = append(unique, skill)
	}
	return unique
}
