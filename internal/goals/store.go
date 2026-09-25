// Package goals stores thread-scoped continuation objectives.
package goals

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	Active   Status = "active"
	Paused   Status = "paused"
	Complete Status = "complete"
	Blocked  Status = "blocked"
)

type Goal struct {
	ID              string    `json:"id"`
	SessionID       string    `json:"session_id"`
	Objective       string    `json:"objective"`
	Status          Status    `json:"status"`
	Turns           int       `json:"turns"`
	NoProgressTurns int       `json:"no_progress_turns,omitempty"`
	BlockerTurns    int       `json:"blocker_turns,omitempty"`
	LastBlocker     string    `json:"last_blocker,omitempty"`
	LastBlockerTurn string    `json:"last_blocker_turn,omitempty"`
	AwaitingUser    bool      `json:"awaiting_user,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) path(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return filepath.Join(s.dir, hex.EncodeToString(digest[:])+".json")
}

func (s *Store) Get(ctx context.Context, sessionID string) (Goal, bool, error) {
	if err := ctx.Err(); err != nil {
		return Goal{}, false, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return Goal{}, false, errors.New("session ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return Goal{}, false, nil
	}
	if err != nil {
		return Goal{}, false, err
	}
	var goal Goal
	if err := json.Unmarshal(data, &goal); err != nil {
		return Goal{}, false, fmt.Errorf("decode goal: %w", err)
	}
	if goal.SessionID != sessionID || strings.TrimSpace(goal.Objective) == "" {
		return Goal{}, false, errors.New("goal record is invalid")
	}
	if goal.ID == "" {
		goal.ID = legacyGenerationID(goal)
	}
	return goal, true, nil
}

func (s *Store) Set(ctx context.Context, sessionID, objective string) (Goal, error) {
	objective = strings.TrimSpace(objective)
	if len(objective) < 8 || len(objective) > 4000 {
		return Goal{}, errors.New("goal objective must be between 8 and 4000 characters")
	}
	return s.update(ctx, sessionID, func() (Goal, error) {
		id, err := newGenerationID()
		if err != nil {
			return Goal{}, fmt.Errorf("create goal generation: %w", err)
		}
		return Goal{ID: id, SessionID: sessionID, Objective: objective, Status: Active, UpdatedAt: time.Now().UTC()}, nil
	}, true)
}

func (s *Store) Pause(ctx context.Context, sessionID string) (Goal, error) {
	return s.change(ctx, sessionID, func(g *Goal) error {
		if g.Status != Active {
			return fmt.Errorf("cannot pause a %s goal", g.Status)
		}
		g.Status, g.AwaitingUser = Paused, false
		return nil
	})
}
func (s *Store) Resume(ctx context.Context, sessionID string) (Goal, error) {
	return s.change(ctx, sessionID, func(g *Goal) error {
		if g.Status != Paused && g.Status != Active {
			return fmt.Errorf("cannot resume a %s goal", g.Status)
		}
		g.Status, g.AwaitingUser, g.Turns, g.NoProgressTurns = Active, false, 0, 0
		return nil
	})
}

// AwaitUser records an explicit cancellation or other deliberate stop without
// changing the saved objective's active/paused identity.
func (s *Store) AwaitUser(ctx context.Context, sessionID string) (Goal, error) {
	return s.change(ctx, sessionID, func(g *Goal) error {
		if g.Status != Active {
			return fmt.Errorf("cannot stop a %s goal", g.Status)
		}
		g.AwaitingUser = true
		return nil
	})
}
func (s *Store) Complete(ctx context.Context, sessionID, evidence string) (Goal, error) {
	if len(strings.TrimSpace(evidence)) < 16 {
		return Goal{}, errors.New("completion requires concrete verification evidence")
	}
	return s.change(ctx, sessionID, func(g *Goal) error {
		if g.Status != Active {
			return fmt.Errorf("cannot complete a %s goal", g.Status)
		}
		g.Status, g.AwaitingUser = Complete, false
		return nil
	})
}

// CompleteGeneration rejects a completion produced by an earlier goal that
// was replaced while its model/tool call was still in flight.
func (s *Store) CompleteGeneration(ctx context.Context, sessionID, generationID, evidence string) (Goal, error) {
	if len(strings.TrimSpace(evidence)) < 16 {
		return Goal{}, errors.New("completion requires concrete verification evidence")
	}
	return s.changeGeneration(ctx, sessionID, generationID, func(g *Goal) error {
		if g.Status != Active {
			return fmt.Errorf("cannot complete a %s goal", g.Status)
		}
		g.Status, g.AwaitingUser = Complete, false
		return nil
	})
}

// ReportBlocker counts one identical blocker per distinct goal turn. Three
// consecutive reports are required before the goal becomes blocked.
func (s *Store) ReportBlocker(ctx context.Context, sessionID, turnID, blocker string) (Goal, error) {
	blocker = strings.TrimSpace(blocker)
	if turnID == "" || len(blocker) < 8 || len(blocker) > 1000 {
		return Goal{}, errors.New("a goal turn ID and concise blocker reason are required")
	}
	return s.change(ctx, sessionID, func(g *Goal) error {
		if g.Status != Active {
			return fmt.Errorf("cannot report a blocker for a %s goal", g.Status)
		}
		if g.LastBlockerTurn == turnID {
			return nil
		}
		if strings.EqualFold(g.LastBlocker, blocker) {
			g.BlockerTurns++
		} else {
			g.LastBlocker, g.BlockerTurns = blocker, 1
		}
		g.LastBlockerTurn = turnID
		if g.BlockerTurns >= 3 {
			g.Status, g.AwaitingUser = Blocked, true
		}
		return nil
	})
}

// ReportBlockerGeneration only applies a blocker to the goal generation that
// issued the tool call.
func (s *Store) ReportBlockerGeneration(ctx context.Context, sessionID, generationID, turnID, blocker string) (Goal, error) {
	blocker = strings.TrimSpace(blocker)
	if turnID == "" || len(blocker) < 8 || len(blocker) > 1000 {
		return Goal{}, errors.New("a goal turn ID and concise blocker reason are required")
	}
	return s.changeGeneration(ctx, sessionID, generationID, func(g *Goal) error {
		if g.Status != Active {
			return fmt.Errorf("cannot report a blocker for a %s goal", g.Status)
		}
		if g.LastBlockerTurn == turnID {
			return nil
		}
		if strings.EqualFold(g.LastBlocker, blocker) {
			g.BlockerTurns++
		} else {
			g.LastBlocker, g.BlockerTurns = blocker, 1
		}
		g.LastBlockerTurn = turnID
		if g.BlockerTurns >= 3 {
			g.Status, g.AwaitingUser = Blocked, true
		}
		return nil
	})
}

// EndTurn tolerates brief no-tool stops, then waits for the user. Productive
// work resets the consecutive no-progress bound and has no arbitrary turn cap.
func (s *Store) EndTurn(ctx context.Context, sessionID, turnID string, toolWork bool) (Goal, bool, error) {
	goal, ok, err := s.Get(ctx, sessionID)
	if err != nil {
		return goal, false, err
	}
	if !ok {
		return Goal{}, false, errors.New("there is no goal for this session")
	}
	return s.EndTurnGeneration(ctx, sessionID, goal.ID, turnID, toolWork)
}

func (s *Store) EndTurnGeneration(ctx context.Context, sessionID, generationID, turnID string, toolWork bool) (Goal, bool, error) {
	g, err := s.changeGeneration(ctx, sessionID, generationID, func(g *Goal) error {
		if g.Status != Active {
			return nil
		}
		g.Turns++
		if g.LastBlockerTurn != "" && g.LastBlockerTurn != turnID {
			g.LastBlocker, g.BlockerTurns = "", 0
		}
		if toolWork {
			g.NoProgressTurns = 0
		} else {
			g.NoProgressTurns++
			if g.NoProgressTurns >= 3 {
				g.AwaitingUser = true
			}
		}
		return nil
	})
	return g, err == nil && g.Status == Active && !g.AwaitingUser, err
}

// Run continues one explicitly started goal through productive turns. The
// executor is called once for the initial user-authorized turn, then again only
// after a tool-work turn; errors, no-tool turns, user pause, and hard limits stop.
func (s *Store) Run(ctx context.Context, sessionID string, execute func(Goal, int, string) (bool, error)) (Goal, error) {
	generationID := ""
	for index := 0; ; index++ {
		if err := ctx.Err(); err != nil {
			return Goal{}, err
		}
		goal, ok, err := s.Get(ctx, sessionID)
		if err != nil {
			return Goal{}, err
		}
		if !ok {
			return Goal{}, errors.New("there is no goal for this session")
		}
		if generationID == "" {
			generationID = goal.ID
		} else if goal.ID != generationID {
			return goal, nil
		}
		if goal.Status != Active || goal.AwaitingUser {
			return goal, nil
		}
		turnID := fmt.Sprintf("%s:%d", sessionID, index)
		worked, runErr := execute(goal, index, turnID)
		if runErr != nil {
			if ctx.Err() != nil {
				latest, ok, _ := s.Get(context.Background(), sessionID)
				if ok {
					return latest, ctx.Err()
				}
				return goal, ctx.Err()
			}
			latest, exists, getErr := s.Get(context.Background(), sessionID)
			if getErr == nil && exists && latest.ID != generationID {
				return latest, nil
			}
			if getErr == nil && exists && latest.ID == generationID && latest.Status == Active {
				_, _ = s.changeGeneration(context.Background(), sessionID, generationID, func(g *Goal) error {
					g.AwaitingUser = true
					return nil
				})
				latest, _, _ = s.Get(context.Background(), sessionID)
			}
			return latest, runErr
		}
		latest, exists, err := s.Get(ctx, sessionID)
		if err != nil {
			return Goal{}, err
		}
		if !exists || latest.Status != Active {
			return latest, nil
		}
		if latest.ID != generationID {
			return latest, nil
		}
		latest, again, err := s.EndTurnGeneration(ctx, sessionID, generationID, turnID, worked)
		if err != nil {
			if errors.Is(err, ErrGoalGenerationChanged) {
				current, ok, getErr := s.Get(ctx, sessionID)
				if getErr != nil {
					return Goal{}, getErr
				}
				if ok {
					return current, nil
				}
			}
			return Goal{}, err
		}
		if !again {
			return latest, nil
		}
	}
}

func (s *Store) Clear(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) change(ctx context.Context, sessionID string, mutate func(*Goal) error) (Goal, error) {
	return s.update(ctx, sessionID, func() (Goal, error) {
		g, ok, err := s.getUnlocked(sessionID)
		if err != nil {
			return Goal{}, err
		}
		if !ok {
			return Goal{}, errors.New("there is no goal for this session")
		}
		if err := mutate(&g); err != nil {
			return Goal{}, err
		}
		g.UpdatedAt = time.Now().UTC()
		return g, nil
	}, false)
}

var ErrGoalGenerationChanged = errors.New("goal was replaced before this operation completed")

func (s *Store) changeGeneration(ctx context.Context, sessionID, generationID string, mutate func(*Goal) error) (Goal, error) {
	return s.update(ctx, sessionID, func() (Goal, error) {
		g, ok, err := s.getUnlocked(sessionID)
		if err != nil {
			return Goal{}, err
		}
		if !ok {
			return Goal{}, errors.New("there is no goal for this session")
		}
		if g.ID != generationID {
			return Goal{}, ErrGoalGenerationChanged
		}
		if err := mutate(&g); err != nil {
			return Goal{}, err
		}
		g.UpdatedAt = time.Now().UTC()
		return g, nil
	}, false)
}

func (s *Store) getUnlocked(sessionID string) (Goal, bool, error) {
	data, err := os.ReadFile(s.path(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return Goal{}, false, nil
	}
	if err != nil {
		return Goal{}, false, err
	}
	var g Goal
	if err := json.Unmarshal(data, &g); err != nil {
		return Goal{}, false, err
	}
	if g.SessionID != sessionID {
		return Goal{}, false, errors.New("goal session ID mismatch")
	}
	if g.ID == "" {
		g.ID = legacyGenerationID(g)
	}
	return g, true, nil
}

func legacyGenerationID(g Goal) string {
	data := []byte(g.SessionID + "\n" + g.Objective + "\n" + g.UpdatedAt.UTC().Format(time.RFC3339Nano))
	sum := sha256.Sum256(data)
	return "legacy-" + hex.EncodeToString(sum[:16])
}

func newGenerationID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func (s *Store) update(ctx context.Context, sessionID string, create func() (Goal, error), replace bool) (Goal, error) {
	if err := ctx.Err(); err != nil {
		return Goal{}, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return Goal{}, errors.New("session ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !replace {
		if _, ok, err := s.getUnlocked(sessionID); err != nil {
			return Goal{}, err
		} else if !ok {
			return Goal{}, errors.New("there is no goal for this session")
		}
	}
	g, err := create()
	if err != nil {
		return Goal{}, err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Goal{}, err
	}
	data, err := json.Marshal(g)
	if err != nil {
		return Goal{}, err
	}
	tmp, err := os.CreateTemp(s.dir, ".goal-*.tmp")
	if err != nil {
		return Goal{}, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return Goal{}, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return Goal{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Goal{}, err
	}
	if err := tmp.Close(); err != nil {
		return Goal{}, err
	}
	if err := os.Rename(name, s.path(sessionID)); err != nil {
		return Goal{}, err
	}
	return g, nil
}
