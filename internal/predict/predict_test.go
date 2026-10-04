package predict

import (
	"testing"

	"github.com/stubbedev/adaptive-power-manager/internal/history"
)

func entries(pairs ...int) []history.Entry {
	out := make([]history.Entry, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, history.Entry{Dow: pairs[i], Minute: pairs[i+1]})
	}
	return out
}

func TestNext(t *testing.T) {
	t.Parallel()
	monday := []int{1, 1020, 1, 1035, 1, 1005, 1, 1050}

	tests := []struct {
		name    string
		entries []history.Entry
		dow     int
		now     int
		want    int
		wantOk  bool
	}{
		{"no history at all", nil, 1, 960, 0, false},
		{"too few samples", entries(1, 1020, 1, 1035), 1, 960, 0, false},
		{"same weekday quartile", entries(monday...), 1, 960, 1005, true},
		{"falls back to every weekday", entries(monday...), 3, 960, 1005, true},
		{"past times are excluded, so late evening holds", entries(monday...), 1, 1380, 0, false},
		{"a same-day sample beat needs its own count", entries(1, 1020, 3, 700, 3, 800, 3, 900), 1, 600, 700, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Next(tt.entries, tt.dow, tt.now, 3)
			if ok != tt.wantOk || got != tt.want {
				t.Errorf("Next() = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.wantOk)
			}
		})
	}
}

func TestNextLowerQuartile(t *testing.T) {
	t.Parallel()
	// Lower quartile of [700, 800, 900, 1000] is the first element, 700:
	// the manager errs towards topping up earlier rather than later.
	got, ok := Next(entries(2, 700, 2, 800, 2, 900, 2, 1000), 2, 600, 3)
	if !ok || got != 700 {
		t.Errorf("Next() = (%d, %v), want (700, true)", got, ok)
	}
}
