// Package skillfrontmatter parses the bounded metadata header used by Agent
// Skills. It reads scalar name/description values only; it never evaluates
// YAML tags or follows aliases.
package skillfrontmatter

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const MaxBytes = 64 << 10

type Metadata struct {
	Name        string
	Description string
}

// Parse extracts name and description from a markdown file with YAML
// frontmatter. Unknown keys are permitted for compatibility with richer skill
// manifests, but their values are bounded and structurally checked.
func Parse(markdown []byte) (Metadata, error) {
	scanner := bufio.NewScanner(bytes.NewReader(markdown))
	scanner.Buffer(make([]byte, 1024), MaxBytes)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return Metadata{}, errors.New("missing YAML frontmatter")
	}
	var yamlLines []string
	frontmatterBytes := len(scanner.Text()) + 1
	closed := false
	for scanner.Scan() {
		line := scanner.Text()
		frontmatterBytes += len(line) + 1
		if frontmatterBytes > MaxBytes {
			return Metadata{}, fmt.Errorf("skill frontmatter exceeds %d bytes", MaxBytes)
		}
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		yamlLines = append(yamlLines, line)
	}
	if err := scanner.Err(); err != nil {
		return Metadata{}, fmt.Errorf("read YAML frontmatter: %w", err)
	}
	if !closed {
		return Metadata{}, errors.New("unterminated YAML frontmatter")
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(yamlLines, "\n")), &document); err != nil {
		return Metadata{}, fmt.Errorf("parse YAML frontmatter: %w", err)
	}
	if err := checkTree(&document, 0, new(int)); err != nil {
		return Metadata{}, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return Metadata{}, errors.New("YAML frontmatter must be a mapping")
	}
	root := document.Content[0]
	var metadata Metadata
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			continue
		}
		if key.Value != "name" && key.Value != "description" {
			continue
		}
		if seen[key.Value] {
			return Metadata{}, fmt.Errorf("duplicate %s field in YAML frontmatter", key.Value)
		}
		seen[key.Value] = true
		if value.Kind != yaml.ScalarNode || (value.Tag != "!!str" && value.Tag != "!!null") {
			return Metadata{}, fmt.Errorf("YAML frontmatter %s must be a scalar", key.Value)
		}
		if value.Tag == "!!null" {
			return Metadata{}, fmt.Errorf("YAML frontmatter %s must not be null", key.Value)
		}
		switch key.Value {
		case "name":
			metadata.Name = strings.TrimSpace(value.Value)
		case "description":
			metadata.Description = strings.TrimSpace(value.Value)
		}
	}
	if strings.TrimSpace(metadata.Name) == "" || strings.TrimSpace(metadata.Description) == "" {
		return Metadata{}, errors.New("frontmatter requires name and description")
	}
	return metadata, nil
}

func checkTree(node *yaml.Node, depth int, count *int) error {
	if depth > 32 {
		return errors.New("YAML frontmatter nesting exceeds 32 levels")
	}
	*count++
	if *count > 4096 {
		return errors.New("YAML frontmatter exceeds 4096 nodes")
	}
	if node.Kind == yaml.AliasNode || node.Alias != nil {
		return errors.New("YAML aliases are not supported in skill frontmatter")
	}
	for _, child := range node.Content {
		if err := checkTree(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}
