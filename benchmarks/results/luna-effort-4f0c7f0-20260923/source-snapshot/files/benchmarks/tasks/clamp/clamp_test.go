package clamp

import "testing"

func TestClampInt(t *testing.T) {
	tests := []struct{ value, lower, upper, want int }{
		{5, 0, 10, 5}, {-1, 0, 10, 0}, {11, 0, 10, 10}, {5, 10, 0, 5}, {-4, 10, 0, 0}, {14, 10, 0, 10},
	}
	for _, test := range tests {
		if got := ClampInt(test.value, test.lower, test.upper); got != test.want {
			t.Errorf("ClampInt(%d, %d, %d) = %d, want %d", test.value, test.lower, test.upper, got, test.want)
		}
	}
}
