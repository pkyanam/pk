package csvcount

import (
	"strings"
	"testing"
)

func TestSumColumn(t *testing.T) {
	got, err := SumColumn(strings.NewReader("name,count\na,12\nb,-2\n"), "count")
	if err != nil || got != 10 {
		t.Fatalf("SumColumn() = %d, %v; want 10, nil", got, err)
	}
}

func TestSumColumnErrors(t *testing.T) {
	for _, input := range []string{"", "name,count\na,nope\n", "name,other\na,1\n"} {
		if _, err := SumColumn(strings.NewReader(input), "count"); err == nil {
			t.Errorf("SumColumn(%q) unexpectedly succeeded", input)
		}
	}
}
