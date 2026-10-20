// Package predict estimates the next unplug time from the recorded
// history. It is a faithful port of the awk model of the bash policy this
// binary replaces.
package predict

import (
	"slices"

	"github.com/stubbedev/adaptive-power-manager/internal/history"
)

// Next returns the minute of day the charger is expected to come out, and
// whether a prediction could be made at all.
//
// Only strictly future times count: once the usual unplug minute has
// passed, the manager holds the base thresholds overnight instead of
// chasing a stale prediction. Times from the same weekday are preferred;
// every weekday fills in until enough same-day samples exist. With fewer
// than minSamples usable records there is no prediction.
func Next(entries []history.Entry, at history.Entry, minSamples int) (int, bool) {
	future := make([]int, 0, len(entries))
	var same []int
	for _, entry := range entries {
		if entry.Minute <= at.Minute {
			continue
		}
		future = append(future, entry.Minute)
		if entry.Dow == at.Dow {
			same = append(same, entry.Minute)
		}
	}

	pool := future
	if len(same) >= minSamples {
		pool = same
	}
	if len(pool) < minSamples {
		return 0, false
	}

	slices.Sort(pool)
	return pool[(len(pool)-1)/4], true
}

// TopUp reports whether the charge ceiling belongs at full at the given
// time: an unplug is predicted within window minutes.
func TopUp(entries []history.Entry, at history.Entry, minSamples, window int) bool {
	predicted, ok := Next(entries, at, minSamples)
	return ok && predicted-at.Minute <= window
}

// NextChange returns the first minute later on the same day at which TopUp
// flips, and false when it holds until midnight.
//
// Next is constant between consecutive recorded unplug minutes, since a
// record only drops out of the pool once its minute is reached. Within
// each such stretch TopUp can only turn on, exactly when the window
// opens; it can only turn off where the prediction itself jumps. So the
// recorded minutes and one window opening per stretch are the only
// candidates, instead of every minute of the day.
func NextChange(entries []history.Entry, at history.Entry, minSamples, window int) (int, bool) {
	current := TopUp(entries, at, minSamples, window)

	edges := make([]int, 0, len(entries)+1)
	for _, entry := range entries {
		if entry.Minute > at.Minute {
			edges = append(edges, entry.Minute)
		}
	}
	slices.Sort(edges)
	edges = append(slices.Compact(edges), history.MinutesPerDay)

	from := at
	for _, edge := range edges {
		if !current {
			if predicted, ok := Next(entries, from, minSamples); ok {
				if opens := predicted - window; opens > from.Minute && opens < edge {
					return opens, true
				}
			}
		}
		if edge == history.MinutesPerDay {
			return 0, false
		}
		from = history.Entry{Dow: at.Dow, Minute: edge}
		if TopUp(entries, from, minSamples, window) != current {
			return edge, true
		}
	}
	return 0, false
}
