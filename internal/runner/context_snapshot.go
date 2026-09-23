package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const contextSnapshotVersion = 1

// ContextSnapshot captures the stable part of a session's model prefix. Model
// and reasoning effort deliberately remain turn settings and are not stored.
type ContextSnapshot struct {
	Version              int
	Workspace            string
	SystemPrompt         string
	IdentityTemplate     string `json:",omitempty"`
	ExplicitSystemPrompt string
	Skills               []ContextSkill
	Tools                []llm.Tool
}

const (
	inheritedHarnessIdentity = "You run on Unreal Agent Harness built by Unreal Labs."
	defaultIdentityTemplate  = "You are pk, a local coding agent running in the pk harness. You run on %s. Identify yourself as pk; distinguish the harness from its model and provider. When asked about your tools, report only the tools available in this session; workspace documentation may describe tools that are not loaded."
)

// ContextSkill stores the skill definition and the exact skill document used
// by this session, so later edits to a source SKILL.md cannot silently rewrite
// the session's prompt prefix or change a deferred SkillUse operation.
type ContextSkill struct {
	Name        string
	Description string
	Path        string
	Content     []byte
}

// ContextSnapshotStore is an optional persistence seam for applications that
// provide their own session store. If omitted, Run stores snapshots alongside
// local sessions or under the workspace's .pk directory.
type ContextSnapshotStore interface {
	LoadContext(context.Context, session.ID) (ContextSnapshot, error)
	SaveContext(context.Context, session.ID, ContextSnapshot) error
}

type fileContextSnapshotStore struct{ directory string }

func (store fileContextSnapshotStore) path(id session.ID) string {
	digest := sha256.Sum256([]byte(id))
	return filepath.Join(store.directory, hex.EncodeToString(digest[:])+".context.json")
}

func (store fileContextSnapshotStore) LoadContext(ctx context.Context, id session.ID) (ContextSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return ContextSnapshot{}, err
	}
	data, err := os.ReadFile(store.path(id))
	if err != nil {
		return ContextSnapshot{}, err
	}
	var snapshot ContextSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ContextSnapshot{}, fmt.Errorf("decode context snapshot: %w", err)
	}
	if snapshot.Version != contextSnapshotVersion {
		return ContextSnapshot{}, fmt.Errorf("unsupported context snapshot version %d", snapshot.Version)
	}
	return snapshot, nil
}

func (store fileContextSnapshotStore) SaveContext(ctx context.Context, id session.ID, snapshot ContextSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(store.directory, 0o700); err != nil {
		return fmt.Errorf("create context snapshot directory: %w", err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode context snapshot: %w", err)
	}
	path := store.path(id)
	temp, err := os.CreateTemp(store.directory, ".context-*.tmp")
	if err != nil {
		return fmt.Errorf("create context snapshot: %w", err)
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("secure context snapshot: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write context snapshot: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync context snapshot: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close context snapshot: %w", err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("publish context snapshot: %w", err)
	}
	return nil
}

func defaultContextSnapshotStore(sessionDir, workspace string) ContextSnapshotStore {
	directory := sessionDir
	if strings.TrimSpace(directory) == "" {
		directory = filepath.Join(workspace, ".pk", "contexts")
	}
	return fileContextSnapshotStore{directory: directory}
}

func snapshotSkills(snapshot ContextSnapshot) []tool.Skill {
	skills := make([]tool.Skill, 0, len(snapshot.Skills))
	for _, skill := range snapshot.Skills {
		skills = append(skills, tool.Skill{Name: skill.Name, Description: skill.Description, Path: skill.Path})
	}
	return skills
}

// identityBuilder decorates only the first system message emitted by the
// upstream builder. All conversation, tool, and operation semantics remain
// owned by the upstream implementation.
type identityBuilder struct {
	contextbuilder.Builder
	template string
}

func (builder identityBuilder) Build() (contextbuilder.Result, error) {
	result, err := builder.Builder.Build()
	if err != nil {
		return result, err
	}
	for i := range result.Request.Input {
		item := &result.Request.Input[i]
		if item.Type != llm.ItemMessage {
			continue
		}
		message, ok := item.Data.(llm.Message)
		if !ok || message.Role != llm.RoleSystem {
			continue
		}
		replaced, found := strings.CutPrefix(message.Text, inheritedHarnessIdentity)
		if !found {
			return contextbuilder.Result{}, fmt.Errorf("upstream system prompt no longer starts with expected harness identity %q", inheritedHarnessIdentity)
		}
		message.Text = fmt.Sprintf(builder.template, result.Request.Model.ID) + replaced
		item.Data = message
		return result, nil
	}
	return contextbuilder.Result{}, errors.New("upstream builder returned no leading system message")
}

var _ contextbuilder.Builder = identityBuilder{}

func captureSkills(skills []tool.Skill) ([]ContextSkill, error) {
	snapshot := make([]ContextSkill, 0, len(skills))
	for _, skill := range skills {
		content, err := os.ReadFile(skill.Path)
		if err != nil {
			return nil, fmt.Errorf("capture skill %q: %w", skill.Name, err)
		}
		snapshot = append(snapshot, ContextSkill{Name: skill.Name, Description: skill.Description, Path: skill.Path, Content: content})
	}
	return snapshot, nil
}

func sameContextSkills(left, right []ContextSkill) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Name != right[index].Name || left[index].Description != right[index].Description ||
			left[index].Path != right[index].Path || !bytes.Equal(left[index].Content, right[index].Content) {
			return false
		}
	}
	return true
}

func isMissingContextSnapshot(err error) bool { return errors.Is(err, os.ErrNotExist) }
