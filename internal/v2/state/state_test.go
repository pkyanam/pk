package state

import (
	"errors"
	"reflect"
	"testing"
)

// fixtureCatalog returns a deterministic local catalog used across tests.
func fixtureCatalog() Catalog {
	return Catalog{
		"openai": {
			{ID: "gpt-6-luna", Provider: "openai", Context: 200_000, Output: 16_384, CostIn: 1.5, CostOut: 7.5},
			{ID: "gpt-4.1", Provider: "openai", Context: 100_000, Output: 8_192, CostIn: 1.0, CostOut: 4.0},
		},
		"anthropic": {
			{ID: "claude-sonnet-4", Provider: "anthropic", Context: 100_000, Output: 8_192, CostIn: 1.0, CostOut: 5.0},
		},
		"sphinctral": {
			{ID: "sphinctral-7b", Provider: "sphinctral", Context: 32_000, Output: 4_096, CostIn: 0.1, CostOut: 0.2},
		},
	}
}

// TestRebuildIsDeterministic verifies the core invariant: the same base +
// same data + same transformation sequence always produce the same value,
// and a second rebuild produces a byte-identical catalog (no compounding).
func TestRebuildIsDeterministic(t *testing.T) {
	load := func() (Catalog, error) { return fixtureCatalog(), nil }
	s := NewCatalog()
	s.Add(TransformCatalog(load))
	s.Add(DisableProviders([]string{"sphinctral"}))
	s.Add(HalveLimits())

	if err := s.Rebuild(); err != nil {
		t.Fatalf("first Rebuild() error = %v", err)
	}
	first := s.Value()
	if err := s.Rebuild(); err != nil {
		t.Fatalf("second Rebuild() error = %v", err)
	}
	second := s.Value()
	if !reflect.DeepEqual(first, second) {
		t.Fatal("rebuild twice gave different catalogs; effect compounded across rebuilds")
	}
}

// TestHalveLimitsRunsOncePerRebuild is the exact lesson from the OpenCode
// post: halving must run once per rebuild, never accumulate.
func TestHalveLimitsRunsOncePerRebuild(t *testing.T) {
	load := func() (Catalog, error) { return fixtureCatalog(), nil }
	s := NewCatalog()
	s.Add(TransformCatalog(load))
	s.Add(HalveLimits())

	for i := 0; i < 3; i++ {
		if err := s.Rebuild(); err != nil {
			t.Fatalf("Rebuild #%d error = %v", i, err)
		}
	}
	got := s.Value()
	if want := int64(200_000 / 2); got["openai"][0].Context != want {
		t.Errorf("Context after 3 rebuilds = %d, want %d (halving must not compound)", got["openai"][0].Context, want)
	}
	if got["openai"][0].Context != 100_000 {
		t.Errorf("openai gpt-6-luna Context = %d, want 100000", got["openai"][0].Context)
	}
	if got["sphinctral"][0].Context != 16_000 {
		t.Errorf("sphinctral Context = %d, want 16000", got["sphinctral"][0].Context)
	}
}

// TestDisableProvidersIsIdempotent verifies re-running (or re-registering)
// the disable transform never changes an already-disabled catalog.
func TestDisableProvidersIsIdempotent(t *testing.T) {
	load := func() (Catalog, error) { return fixtureCatalog(), nil }
	s := NewCatalog()
	s.Add(TransformCatalog(load))
	s.Add(DisableProviders([]string{"sphinctral"}))
	s.Add(DisableProviders([]string{"sphinctral"})) // duplicate registration

	if err := s.Rebuild(); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if !s.Value()["sphinctral"][0].Disabled {
		t.Error("sphinctral model not disabled")
	}
	if s.Value()["openai"][0].Disabled {
		t.Error("openai model should not be disabled")
	}
	// Rebuild again: still disabled exactly once, never toggled back.
	if err := s.Rebuild(); err != nil {
		t.Fatalf("second Rebuild() error = %v", err)
	}
	if !s.Value()["sphinctral"][0].Disabled {
		t.Error("sphinctral model lost Disabled after second rebuild")
	}
}

// TestCleanupMissing verifies providers absent from the current data source
// are removed while known providers are kept.
func TestCleanupMissing(t *testing.T) {
	s := NewCatalog()
	s.Add(TransformCatalog(func() (Catalog, error) { return fixtureCatalog(), nil }))
	s.Add(CleanupMissing([]string{"openai", "anthropic"}))

	if err := s.Rebuild(); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	got := s.Value()
	if _, ok := got["sphinctral"]; ok {
		t.Error("sphinctral should have been removed by CleanupMissing")
	}
	if _, ok := got["openai"]; !ok {
		t.Error("openai should be kept by CleanupMissing")
	}
	if _, ok := got["anthropic"]; !ok {
		t.Error("anthropic should be kept by CleanupMissing")
	}
}

// TestTransformsDoNotMutateInput verifies a transform's clone discipline: the
// value published by an earlier rebuild is not corrupted by a later one.
func TestTransformsDoNotMutateInput(t *testing.T) {
	load := func() (Catalog, error) { return fixtureCatalog(), nil }
	s := NewCatalog()
	s.Add(TransformCatalog(load))
	s.Add(HalveLimits())
	if err := s.Rebuild(); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	before := s.Value()
	if err := s.Rebuild(); err != nil {
		t.Fatalf("second Rebuild() error = %v", err)
	}
	if !reflect.DeepEqual(before, s.Value()) {
		t.Fatal("value changed between rebuilds; a transform mutated its input")
	}
}

// TestOnUpdateNotifiedAfterRebuild verifies subscribers receive each new
// value after a successful rebuild, and that they are not called on failure.
func TestOnUpdateNotifiedAfterRebuild(t *testing.T) {
	s := New[int](0)
	var seen []int
	s.OnUpdate(func(v int) { seen = append(seen, v) })
	s.Add(func(v int) (int, error) { return v + 1, nil })

	if err := s.Rebuild(); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if err := s.Rebuild(); err != nil {
		t.Fatalf("second Rebuild() error = %v", err)
	}
	if !reflect.DeepEqual(seen, []int{1, 1}) {
		t.Fatalf("subscriber saw %v, want [1 1]", seen)
	}

	// A failing transform leaves the value untouched and skips subscribers.
	boom := New[int](0)
	boom.OnUpdate(func(int) { t.Error("subscriber called after failed rebuild") })
	boom.Add(func(v int) (int, error) { return v + 1, nil })
	boom.Add(func(int) (int, error) { return 0, errors.New("boom") })
	if err := boom.Rebuild(); err == nil {
		t.Fatal("Rebuild() succeeded, want error")
	}
	if boom.Value() != 0 {
		t.Fatalf("value after failed rebuild = %d, want 0", boom.Value())
	}
}

// TestStateAddNilIsSafe verifies Add ignores nil transforms.
func TestStateAddNilIsSafe(t *testing.T) {
	s := New[int](0)
	s.Add(nil)
	if err := s.Rebuild(); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if s.Value() != 0 {
		t.Fatalf("value = %d, want 0", s.Value())
	}
}
