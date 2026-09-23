package extensions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	ProtocolVersion            = "pk.extensions/v1"
	maxManifestSize            = 1 << 20
	maxMessageSize             = 1 << 20
	maxCommandDescriptionBytes = 1024
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
var toolNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
var semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

var supportedCapabilities = map[string]struct{}{"workspace.read": {}}

// Manifest declares an explicitly loaded, out-of-process extension. It is a
// registration contract, not an operating-system sandbox policy.
type Manifest struct {
	APIVersion   string        `json:"api_version"`
	ID           string        `json:"id"`
	Version      string        `json:"version"`
	Executable   string        `json:"executable"`
	Args         []string      `json:"args,omitempty"`
	Capabilities []string      `json:"capabilities,omitempty"`
	Tools        []ToolSpec    `json:"tools,omitempty"`
	Commands     []CommandSpec `json:"commands,omitempty"`
	Hooks        []HookSpec    `json:"hooks,omitempty"`
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type CommandSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type HookSpec struct {
	Event string `json:"event"`
	Mode  string `json:"mode"` // observe or transform
}

// LoadManifest reads only the named file. The host never discovers extensions
// by scanning a repository or workspace.
func LoadManifest(path string) (Manifest, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve extension manifest: %w", err)
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect extension manifest: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Manifest{}, errors.New("extension manifest must be a regular file")
	}
	file, err := os.Open(absolutePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open extension manifest: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxManifestSize+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read extension manifest: %w", err)
	}
	if len(data) > maxManifestSize {
		return Manifest{}, fmt.Errorf("extension manifest exceeds %d bytes", maxManifestSize)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode extension manifest: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Manifest{}, errors.New("extension manifest must contain exactly one JSON object")
	}
	if strings.TrimSpace(manifest.Executable) == "" {
		return Manifest{}, fmt.Errorf("extension %q has no executable", manifest.ID)
	}
	if !filepath.IsAbs(manifest.Executable) && (strings.ContainsRune(manifest.Executable, filepath.Separator) || (filepath.Separator != '/' && strings.ContainsRune(manifest.Executable, '/'))) {
		manifest.Executable = filepath.Join(filepath.Dir(absolutePath), manifest.Executable)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	if m.APIVersion != ProtocolVersion {
		return fmt.Errorf("extension %q uses unsupported API %q", m.ID, m.APIVersion)
	}
	if !namePattern.MatchString(m.ID) {
		return fmt.Errorf("invalid extension ID %q", m.ID)
	}
	if !semverPattern.MatchString(m.Version) {
		return fmt.Errorf("extension %q version %q must be semantic version X.Y.Z", m.ID, m.Version)
	}
	if strings.TrimSpace(m.Executable) == "" {
		return fmt.Errorf("extension %q has no executable", m.ID)
	}
	seenCapabilities := make(map[string]struct{}, len(m.Capabilities))
	for _, capability := range m.Capabilities {
		if !namePattern.MatchString(capability) {
			return fmt.Errorf("extension %q has invalid capability %q", m.ID, capability)
		}
		if _, exists := seenCapabilities[capability]; exists {
			return fmt.Errorf("extension %q repeats capability %q", m.ID, capability)
		}
		seenCapabilities[capability] = struct{}{}
		if _, ok := supportedCapabilities[capability]; !ok {
			return fmt.Errorf("extension %q requests unsupported capability %q", m.ID, capability)
		}
	}
	seen := make(map[string]string)
	for _, spec := range m.Tools {
		if !toolNamePattern.MatchString(spec.Name) {
			return fmt.Errorf("extension %q has invalid tool name %q", m.ID, spec.Name)
		}
		if err := claimName(seen, "tool", spec.Name); err != nil {
			return fmt.Errorf("extension %q: %w", m.ID, err)
		}
		var schema map[string]any
		if len(spec.Parameters) == 0 || json.Unmarshal(spec.Parameters, &schema) != nil || schema == nil || schema["type"] != "object" {
			return fmt.Errorf("extension %q tool %q parameters must be an object JSON Schema", m.ID, spec.Name)
		}
		if strings.TrimSpace(spec.Description) == "" {
			return fmt.Errorf("extension %q tool %q has no description", m.ID, spec.Name)
		}
	}
	for _, spec := range m.Commands {
		if !namePattern.MatchString(spec.Name) {
			return fmt.Errorf("extension %q has invalid command name %q", m.ID, spec.Name)
		}
		if strings.TrimSpace(spec.Description) == "" {
			return fmt.Errorf("extension %q command %q has no description", m.ID, spec.Name)
		}
		if len(spec.Description) > maxCommandDescriptionBytes {
			return fmt.Errorf("extension %q command %q description exceeds %d bytes", m.ID, spec.Name, maxCommandDescriptionBytes)
		}
		if err := claimName(seen, "command", spec.Name); err != nil {
			return fmt.Errorf("extension %q: %w", m.ID, err)
		}
	}
	for _, spec := range m.Hooks {
		return fmt.Errorf("extension %q declares hook %q, but lifecycle hooks are not supported by protocol v1", m.ID, spec.Event)
	}
	return nil
}

func claimName(seen map[string]string, kind, name string) error {
	key := kind + ":" + name
	if prior, exists := seen[key]; exists {
		return fmt.Errorf("duplicate %s name %q", prior, name)
	}
	seen[key] = kind
	return nil
}
