package goals

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestRunDoesNotApplyOldTurnResultsToReplacementGoal(t *testing.T) {
	for _, runErr := range []error{nil, errors.New("old goal runner failed")} {
		t.Run(fmt.Sprint(runErr), func(t *testing.T) {
			ctx := context.Background()
			store := NewStore(t.TempDir())
			old, err := store.Set(ctx, "thread-race", "Finish the old verification task")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			got, err := store.Run(ctx, "thread-race", func(goal Goal, _ int, _ string) (bool, error) {
				calls++
				if goal.ID != old.ID {
					t.Fatalf("runner received generation %q, want %q", goal.ID, old.ID)
				}
				if _, err := store.Set(ctx, "thread-race", "Build the replacement runnable beta task"); err != nil {
					t.Fatal(err)
				}
				return true, runErr
			})
			if err != nil {
				t.Fatalf("Run returned old-generation error: %v", err)
			}
			current, ok, getErr := store.Get(ctx, "thread-race")
			if getErr != nil || !ok {
				t.Fatalf("replacement missing: ok=%t err=%v", ok, getErr)
			}
			if calls != 1 || got.ID != current.ID || got.ID == old.ID || got.Status != Active || got.Turns != 0 || got.AwaitingUser {
				t.Fatalf("old run mutated or retried replacement: calls=%d result=%#v current=%#v", calls, got, current)
			}
		})
	}
}

func TestGenerationScopedCompletionAndBlockerRejectStaleGeneration(t *testing.T) {
	ctx := context.Background()
	store := NewStore(t.TempDir())
	old, err := store.Set(ctx, "thread-stale", "Verify the old milestone")
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Set(ctx, "thread-stale", "Build the new runnable beta task")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteGeneration(ctx, "thread-stale", old.ID, "old task test output was successful"); !errors.Is(err, ErrGoalGenerationChanged) {
		t.Fatalf("stale completion error=%v, want generation mismatch", err)
	}
	if _, err := store.ReportBlockerGeneration(ctx, "thread-stale", old.ID, "old-turn", "old provider unavailable"); !errors.Is(err, ErrGoalGenerationChanged) {
		t.Fatalf("stale blocker error=%v, want generation mismatch", err)
	}
	got, ok, err := store.Get(ctx, "thread-stale")
	if err != nil || !ok || got.ID != current.ID || got.Status != Active || got.BlockerTurns != 0 || got.Turns != 0 {
		t.Fatalf("stale operations changed replacement: %#v, ok=%t err=%v", got, ok, err)
	}
}

func TestGoalSurvivesRestartPauseAndResume(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := NewStore(dir)
	created, err := s.Set(ctx, "thread-1", "Finish and verify the daemon milestone")
	if err != nil || created.Status != Active {
		t.Fatalf("create: %+v %v", created, err)
	}
	// A new Store models a process restart: persisted state is still available,
	// but callers must explicitly Resume before starting another goal turn.
	s = NewStore(dir)
	g, ok, err := s.Get(ctx, "thread-1")
	if err != nil || !ok || g.Objective != created.Objective {
		t.Fatalf("reload: %+v %t %v", g, ok, err)
	}
	g, err = s.Pause(ctx, "thread-1")
	if err != nil || g.Status != Paused {
		t.Fatalf("pause: %+v %v", g, err)
	}
	if _, err = s.Complete(ctx, "thread-1", "Tests passed."); err == nil {
		t.Fatal("paused goal completed")
	}
	g, err = s.Resume(ctx, "thread-1")
	if err != nil || g.Status != Active || g.AwaitingUser {
		t.Fatalf("resume: %+v %v", g, err)
	}
}

func TestGoalCompletionRequiresEvidenceAndBlockerRepeatsAcrossTurns(t *testing.T) {
	ctx := context.Background()
	s := NewStore(t.TempDir())
	_, err := s.Set(ctx, "thread-2", "Investigate the saved daemon verification task")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Complete(ctx, "thread-2", "done"); err == nil {
		t.Fatal("completion without evidence accepted")
	}
	for i := 1; i <= 3; i++ {
		g, err := s.ReportBlocker(ctx, "thread-2", "turn-"+string(rune('0'+i)), "The configured provider is unavailable")
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 && g.Status != Active {
			t.Fatalf("blocked early on turn %d", i)
		}
		if i == 3 && g.Status != Blocked {
			t.Fatalf("status after repeated blocker = %s", g.Status)
		}
	}
}

func TestNoProgressWaitsForUserAndProductiveWorkKeepsGoing(t *testing.T) {
	ctx := context.Background()
	s := NewStore(t.TempDir())
	_, err := s.Set(ctx, "thread", "Verify the requested end state")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		g, again, err := s.EndTurn(ctx, "thread", fmt.Sprint("stalled", i), false)
		if err != nil || again != (i < 3) {
			t.Fatalf("no-progress turn %d: again=%t err=%v", i, again, err)
		}
		if (i == 3) != g.AwaitingUser {
			t.Fatalf("awaiting user after no-progress turn %d: %+v", i, g)
		}
	}
	if _, err := s.Resume(ctx, "thread"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, again, err := s.EndTurn(ctx, "thread", fmt.Sprint(i), true); err != nil || !again {
			t.Fatalf("productive turn %d stopped: again=%t err=%v", i, again, err)
		}
	}
}

func TestRunInvokesSecondTurnOnlyAfterToolWorkAndStopsOnCompletion(t *testing.T) {
	ctx := context.Background()
	s := NewStore(t.TempDir())
	_, err := s.Set(ctx, "thread-run", "Complete the verified multi-step objective")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	goal, err := s.Run(ctx, "thread-run", func(g Goal, index int, turnID string) (bool, error) {
		calls++
		if index == 0 {
			if turnID == "" {
				t.Fatal("missing turn ID")
			}
			return true, nil
		}
		_, err := s.Complete(ctx, g.SessionID, "Relevant test suite passed and the expected output was confirmed.")
		return true, err
	})
	if err != nil || calls != 2 || goal.Status != Complete {
		t.Fatalf("goal=%+v calls=%d err=%v", goal, calls, err)
	}
}

func TestRunStopsAfterNoProgressAndProviderError(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{{"no progress", false}, {"provider error", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := NewStore(t.TempDir())
			_, err := s.Set(ctx, "thread-stop", "Finish the requested verification")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, runErr := s.Run(ctx, "thread-stop", func(Goal, int, string) (bool, error) {
				calls++
				if tc.fail {
					return false, fmt.Errorf("provider unavailable")
				}
				return false, nil
			})
			if (runErr != nil) != tc.fail || calls != map[bool]int{false: 3, true: 1}[tc.fail] {
				t.Fatalf("calls=%d err=%v", calls, runErr)
			}
			g, ok, err := s.Get(ctx, "thread-stop")
			if err != nil || !ok || !g.AwaitingUser || g.Status != Active {
				t.Fatalf("state=%+v ok=%t err=%v", g, ok, err)
			}
		})
	}
}

func TestPauseAndCancellationStopBeforeAnotherGoalTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := NewStore(t.TempDir())
	if _, err := s.Set(ctx, "thread-cancel", "Finish the requested verification"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	goal, err := s.Run(ctx, "thread-cancel", func(g Goal, _ int, _ string) (bool, error) {
		calls++
		if _, pauseErr := s.Pause(context.Background(), g.SessionID); pauseErr != nil {
			return false, pauseErr
		}
		cancel()
		return false, ctx.Err()
	})
	if err == nil || calls != 1 || goal.Status != Paused {
		t.Fatalf("goal=%+v calls=%d err=%v", goal, calls, err)
	}
}
