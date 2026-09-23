package intervals

import "testing"

func TestCoveredCountUsesClosedRangesAndMergesOverlap(t *testing.T) {
	tests := []struct {
		name   string
		ranges []Range
		want   int
	}{
		{name: "single point", ranges: []Range{{Start: 7, End: 7}}, want: 1},
		{name: "overlap", ranges: []Range{{Start: 2, End: 5}, {Start: 4, End: 8}}, want: 7},
		{name: "disjoint", ranges: []Range{{Start: 5, End: 6}, {Start: 1, End: 3}}, want: 5},
		{name: "empty input", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CoveredCount(tt.ranges)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("CoveredCount(%v) = %d, want %d", tt.ranges, got, tt.want)
			}
		})
	}
}

func TestCoveredCountRejectsInvertedRange(t *testing.T) {
	if _, err := CoveredCount([]Range{{Start: 4, End: 2}}); err == nil {
		t.Fatal("expected inverted range to be rejected")
	}
}
