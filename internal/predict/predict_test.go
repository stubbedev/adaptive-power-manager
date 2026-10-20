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
			got, ok := Next(tt.entries, history.Entry{Dow: tt.dow, Minute: tt.now}, 3)
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
	got, ok := Next(entries(2, 700, 2, 800, 2, 900, 2, 1000), history.Entry{Dow: 2, Minute: 600}, 3)
	if !ok || got != 700 {
		t.Errorf("Next() = (%d, %v), want (700, true)", got, ok)
	}
}

// bruteNextChange is the definition NextChange optimizes: TopUp evaluated
// at every later minute of the day.
func bruteNextChange(entries []history.Entry, at history.Entry, minSamples, window int) (int, bool) {
	current := TopUp(entries, at, minSamples, window)
	for minute := at.Minute + 1; minute < history.MinutesPerDay; minute++ {
		if TopUp(entries, history.Entry{Dow: at.Dow, Minute: minute}, minSamples, window) != current {
			return minute, true
		}
	}
	return 0, false
}

func TestNextChangeMatchesEveryMinute(t *testing.T) {
	t.Parallel()
	histories := map[string][]history.Entry{
		"empty":            nil,
		"one weekday":      entries(1, 1020, 1, 1035, 1, 1005, 1, 1050),
		"mixed weekdays":   entries(1, 1020, 3, 700, 3, 800, 3, 900, 2, 450, 5, 1439, 1, 0),
		"duplicate minute": entries(1, 600, 1, 600, 1, 600, 2, 600),
		"spread":           entries(1, 60, 1, 300, 1, 420, 1, 480, 1, 1200, 1, 1300, 4, 30, 4, 1400),
	}
	for name, hist := range histories {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for dow := 1; dow <= 7; dow++ {
				for minute := range history.MinutesPerDay {
					at := history.Entry{Dow: dow, Minute: minute}
					got, gotOk := NextChange(hist, at, 3, 120)
					want, wantOk := bruteNextChange(hist, at, 3, 120)
					if got != want || gotOk != wantOk {
						t.Fatalf("NextChange(%+v) = (%d, %v), want (%d, %v)", at, got, gotOk, want, wantOk)
					}
				}
			}
		})
	}
}

func TestNextChangeOpensWindow(t *testing.T) {
	t.Parallel()
	// Lower quartile 1005 with a 120-minute window opens at 885.
	got, ok := NextChange(entries(1, 1020, 1, 1035, 1, 1005, 1, 1050), history.Entry{Dow: 1, Minute: 600}, 3, 120)
	if !ok || got != 885 {
		t.Errorf("NextChange() = (%d, %v), want (885, true)", got, ok)
	}
}
