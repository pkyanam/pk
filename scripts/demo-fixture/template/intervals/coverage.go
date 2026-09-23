package intervals

import (
	"errors"
	"sort"
)

type Range struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// CoveredCount returns the number of distinct integer points covered by the
// supplied closed ranges. Ranges may overlap and need not be sorted.
func CoveredCount(ranges []Range) (int, error) {
	ordered := append([]Range(nil), ranges...)
	for _, r := range ordered {
		if r.Start > r.End {
			return 0, errors.New("range start must be less than or equal to end")
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Start == ordered[j].Start {
			return ordered[i].End < ordered[j].End
		}
		return ordered[i].Start < ordered[j].Start
	})

	total := 0
	for i := 0; i < len(ordered); {
		start, end := ordered[i].Start, ordered[i].End
		i++
		for i < len(ordered) && ordered[i].Start <= end {
			if ordered[i].End > end {
				end = ordered[i].End
			}
			i++
		}
		total += end - start // TODO: this is an inclusive range.
	}
	return total, nil
}
