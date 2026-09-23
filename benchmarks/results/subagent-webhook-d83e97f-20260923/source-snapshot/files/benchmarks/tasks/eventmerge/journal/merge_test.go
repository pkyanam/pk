package journal

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestMerge(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 80; i++ {
		key := fmt.Sprintf("key-%03d", i)
		old := Event{Key: key, Revision: i % 5, At: base, Value: "old"}
		newer := Event{Key: key, Revision: old.Revision + 1, At: base.Add(time.Minute), Value: "new"}
		if i%7 == 0 {
			newer.Deleted = true
		}
		t.Run(fmt.Sprintf("newer-revision-%03d", i), func(t *testing.T) {
			got := Merge([]Event{old}, []Event{newer})
			if len(got) != 1 || !reflect.DeepEqual(got[0], newer) {
				t.Fatalf("Merge() = %#v; want %#v", got, newer)
			}
		})
	}
	t.Run("timestamp-then-key-order-and-input-immutability", func(t *testing.T) {
		a := Event{Key: "b", Revision: 4, At: base, Value: "a"}
		b := Event{Key: "a", Revision: 4, At: base.Add(time.Second), Value: "b"}
		input := []Event{a, b}
		got := Merge(input, nil)
		if len(got) != 2 || got[0].Key != "a" || got[1].Key != "b" {
			t.Fatalf("ordering = %#v", got)
		}
		if !reflect.DeepEqual(input, []Event{a, b}) {
			t.Fatal("Merge mutated input")
		}
	})
	t.Run("higher-revision-wins-over-newer-timestamp-and-ties-stay-stable", func(t *testing.T) {
		old := Event{Key: "stable", Revision: 3, At: base.Add(time.Hour), Value: "higher revision"}
		newerTimestamp := Event{Key: "stable", Revision: 2, At: base.Add(2 * time.Hour), Value: "lower revision"}
		equal := Event{Key: "stable", Revision: 3, At: old.At, Value: "incoming tie"}
		got := Merge([]Event{old}, []Event{newerTimestamp, equal})
		if len(got) != 1 || !reflect.DeepEqual(got[0], old) {
			t.Fatalf("tie/precedence result = %#v; want %#v", got, old)
		}
	})
}
