// Package history keeps the log of when the charger came out, which is the
// training data for the unplug-time prediction.
package history

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Entry is one recorded unplug: ISO weekday (1 = Monday .. 7 = Sunday) and
// minute of the local day.
type Entry struct {
	Dow    int
	Minute int
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&sb, "%d %d\n", e.Dow, e.Minute)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
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
	if err != nil || m < 0 || m >= 24*60 {
		return Entry{}, false
	}
	return Entry{Dow: d, Minute: m}, true
}
