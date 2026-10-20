// Package history keeps the log of when the charger came out, which is the
// training data for the unplug-time prediction.
package history

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stubbedev/adaptive-power-manager/internal/statefile"
)

// MinutesPerDay bounds Entry.Minute.
const MinutesPerDay = 24 * 60

// Entry is one recorded unplug: ISO weekday (1 = Monday .. 7 = Sunday) and
// minute of the local day.
type Entry struct {
	Dow    int
	Minute int
}

// At places t on the local weekly clock the history is recorded in.
func At(t time.Time) Entry {
	return Entry{
		Dow:    (int(t.Weekday())+6)%7 + 1,
		Minute: t.Hour()*60 + t.Minute(),
	}
}

// Load reads the unplug log. A missing file is an empty history, not an
// error; malformed lines are skipped so a truncated write cannot poison
// the model.
func Load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		entry, ok := parse(scanner.Text())
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return entries, nil
}

// Add records one unplug and trims the log to the newest keep entries.
func Add(path string, entry Entry, keep int) error {
	entries, err := Load(path)
	if err != nil {
		return err
	}
	entries = append(entries, entry)
	if len(entries) > keep {
		entries = entries[len(entries)-keep:]
	}
	buf := make([]byte, 0, len(entries)*len("7 1439\n"))
	for _, e := range entries {
		buf = strconv.AppendInt(buf, int64(e.Dow), 10)
		buf = append(buf, ' ')
		buf = strconv.AppendInt(buf, int64(e.Minute), 10)
		buf = append(buf, '\n')
	}
	return statefile.Write(path, buf)
}

func parse(line string) (Entry, bool) {
	dow, minute, ok := strings.Cut(strings.TrimSpace(line), " ")
	if !ok {
		return Entry{}, false
	}
	d, err := strconv.Atoi(dow)
	if err != nil || d < 1 || d > 7 {
		return Entry{}, false
	}
	m, err := strconv.Atoi(minute)
	if err != nil || m < 0 || m >= MinutesPerDay {
		return Entry{}, false
	}
	return Entry{Dow: d, Minute: m}, true
}
