package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkyanam/pk/internal/skillfrontmatter"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const maxSkillReadBytes = 1 << 20

type SkillCatalog struct {
	Skills   []SkillCatalogEntry `json:"skills"`
	Warnings []string            `json:"warnings"`
	Saved    bool                `json:"saved"`
}

type SkillCatalogEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Bundled     bool   `json:"bundled"`
	Saved       bool   `json:"saved"`
}

type SkillDocument struct {
	Skill   SkillCatalogEntry `json:"skill"`
	Content string            `json:"content"`
}

// LoadSkillCatalog returns the exact skill catalog captured for a saved
// session. If no snapshot exists, it discovers the currently configured dirs.
func LoadSkillCatalog(ctx context.Context, sessionDir, sessionID string, dirs []string, workspace string) (SkillCatalog, error) {
	if err := ctx.Err(); err != nil {
		return SkillCatalog{}, err
	}
	if strings.TrimSpace(sessionID) != "" {
		store := defaultContextSnapshotStore(sessionDir, workspace)
		snapshot, err := store.LoadContext(ctx, session.ID(sessionID))
		if err == nil {
			catalog := SkillCatalog{Saved: true, Skills: make([]SkillCatalogEntry, 0, len(snapshot.Skills)), Warnings: []string{}}
			for _, skill := range snapshot.Skills {
				if err := ctx.Err(); err != nil {
					return SkillCatalog{}, err
				}
				catalog.Skills = append(catalog.Skills, catalogEntry(skill.Name, skill.Description, skill.Path, true, sessionDir))
			}
			return catalog, nil
		}
		if !isMissingContextSnapshot(err) {
			return SkillCatalog{}, fmt.Errorf("load saved skill catalog: %w", err)
		}
	}

	skills, warnings, err := discoverCatalogSkills(ctx, dirs)
	if err != nil {
		return SkillCatalog{}, err
	}
	catalog := SkillCatalog{Skills: make([]SkillCatalogEntry, 0, len(skills)), Warnings: warnings}
	for _, skill := range skills {
		catalog.Skills = append(catalog.Skills, catalogEntry(skill.Name, skill.Description, skill.Path, false, sessionDir))
	}
	return catalog, nil
}

// ReadSkill resolves name through the registered catalog. Saved sessions read
// the frozen snapshot content; fresh catalogs read only the matching file.
func ReadSkill(ctx context.Context, sessionDir, sessionID string, dirs []string, workspace, name string) (SkillDocument, error) {
	if err := ctx.Err(); err != nil {
		return SkillDocument{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return SkillDocument{}, errors.New("skill name must not be empty")
	}
	if strings.TrimSpace(sessionID) != "" {
		store := defaultContextSnapshotStore(sessionDir, workspace)
		snapshot, err := store.LoadContext(ctx, session.ID(sessionID))
		if err == nil {
			for _, skill := range snapshot.Skills {
				if err := ctx.Err(); err != nil {
					return SkillDocument{}, err
				}
				if skill.Name == name {
					return SkillDocument{
						Skill:   catalogEntry(skill.Name, skill.Description, skill.Path, true, sessionDir),
						Content: string(skill.Content),
					}, nil
				}
			}
			return SkillDocument{}, fmt.Errorf("skill %q is not registered in this session", name)
		}
		if !isMissingContextSnapshot(err) {
			return SkillDocument{}, fmt.Errorf("load saved skill document: %w", err)
		}
	}

	skills, _, err := discoverCatalogSkills(ctx, dirs)
	if err != nil {
		return SkillDocument{}, err
	}
	for _, skill := range skills {
		if err := ctx.Err(); err != nil {
			return SkillDocument{}, err
		}
		if skill.Name != name {
			continue
		}
		info, err := os.Stat(skill.Path)
		if err != nil {
			return SkillDocument{}, fmt.Errorf("stat skill %q: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return SkillDocument{}, fmt.Errorf("skill %q is not a regular file", name)
		}
		file, err := os.Open(skill.Path)
		if err != nil {
			return SkillDocument{}, fmt.Errorf("open skill %q: %w", name, err)
		}
		info, err = file.Stat()
		if err != nil {
			_ = file.Close()
			return SkillDocument{}, fmt.Errorf("stat skill %q: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			_ = file.Close()
			return SkillDocument{}, fmt.Errorf("skill %q is not a regular file", name)
		}
		content, readErr := io.ReadAll(io.LimitReader(file, maxSkillReadBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return SkillDocument{}, fmt.Errorf("read skill %q: %w", name, readErr)
		}
		if closeErr != nil {
			return SkillDocument{}, fmt.Errorf("close skill %q: %w", name, closeErr)
		}
		if len(content) > maxSkillReadBytes {
			return SkillDocument{}, fmt.Errorf("skill %q exceeds the %d-byte read limit", name, maxSkillReadBytes)
		}
		if err := ctx.Err(); err != nil {
			return SkillDocument{}, err
		}
		return SkillDocument{Skill: catalogEntry(skill.Name, skill.Description, skill.Path, false, sessionDir), Content: string(content)}, nil
	}
	return SkillDocument{}, fmt.Errorf("skill %q is not registered", name)
}

func discoverCatalogSkills(ctx context.Context, dirs []string) ([]tool.Skill, []string, error) {
	var skills []tool.Skill
	warnings := []string{}
	seenPaths := make(map[string]struct{})
	seenNames := make(map[string]string)
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, warnings, err
		}
		discovered, errs := tool.DiscoverSkills(dir)
		for _, err := range errs {
			warnings = append(warnings, err.Error())
		}
		for _, skill := range discovered {
			metadata, metadataErr := loadSkillMetadata(skill.Path)
			if metadataErr != nil {
				warnings = append(warnings, fmt.Sprintf("read skill metadata %q: %v; ignored", skill.Path, metadataErr))
				continue
			}
			skill.Name = metadata.Name
			skill.Description = metadata.Description
			path := canonicalSkillPath(skill.Path)
			if _, exists := seenPaths[path]; exists {
				continue
			}
			seenPaths[path] = struct{}{}
			if prior, exists := seenNames[skill.Name]; exists {
				warnings = append(warnings, fmt.Sprintf("skill %q from %q conflicts with the registered skill at %q; ignored", skill.Name, skill.Path, prior))
				continue
			}
			seenNames[skill.Name] = skill.Path
			skills = append(skills, skill)
		}
	}
	return skills, warnings, nil
}

func loadSkillMetadata(path string) (skillfrontmatter.Metadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return skillfrontmatter.Metadata{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, skillfrontmatter.MaxBytes+1))
	if err != nil {
		return skillfrontmatter.Metadata{}, err
	}
	return skillfrontmatter.Parse(data)
}

func catalogEntry(name, description, path string, saved bool, sessionDir string) SkillCatalogEntry {
	canonical := canonicalSkillPath(path)
	bundledRoot := ""
	if sessionDir != "" {
		bundledRoot = canonicalSkillPath(filepath.Join(filepath.Dir(sessionDir), "bundled-skills"))
	}
	bundled := false
	if bundledRoot != "" {
		rel, err := filepath.Rel(bundledRoot, canonical)
		bundled = err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	return SkillCatalogEntry{Name: name, Description: description, Path: path, Bundled: bundled, Saved: saved}
}

func canonicalSkillPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(absolute)
}
