package intervals

import (
	"reflect"
	"testing"
)

func TestMerge(t *testing.T) {
	input := []Interval{{8, 10}, {1, 3}, {2, 5}, {10, 12}, {20, 21}}
	want := []Interval{{1, 5}, {8, 12}, {20, 21}}
	if got := Merge(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge() = %#v, want %#v", got, want)
	}
	if got := Merge(nil); got == nil || len(got) != 0 {
		t.Fatalf("Merge(nil) = %#v, want a non-nil empty slice", got)
	}
}
