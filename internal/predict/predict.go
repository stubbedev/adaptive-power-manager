// Package predict estimates the next unplug time from the recorded
// history. It is a faithful port of the awk model of the bash policy this
// binary replaces.
package predict

import (
	"sort"

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
func Next(entries []history.Entry, dow, nowMinute, minSamples int) (int, bool) {
	var future, same []int
	for _, entry := range entries {
		if entry.Minute <= nowMinute {
			continue
		}
		future = append(future, entry.Minute)
		if entry.Dow == dow {
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

	sort.Ints(pool)
	return pool[(len(pool)-1)/4], true
}
