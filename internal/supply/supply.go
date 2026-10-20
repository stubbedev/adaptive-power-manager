// Package supply reads and writes the kernel's power supply interfaces: AC
// presence from /sys/class/power_supply and the battery charge thresholds
// the embedded controller honors.
package supply

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	startAttr = "charge_control_start_threshold"
	endAttr   = "charge_control_end_threshold"
)

// Supply addresses one sysfs power supply class directory.
type Supply struct {
	Dir string
}

// Battery names one discovered battery and its sysfs directory.
type Battery struct {
	Name string
	Path string
}

// OnAC reports whether any mains or USB-C source is currently online. Both
// types matter: a USB-C-only charger may never fire a Mains event.
func (s Supply) OnAC() (bool, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", s.Dir, err)
	}
	for _, entry := range entries {
		dir := filepath.Join(s.Dir, entry.Name())
		typ, err := readTrim(filepath.Join(dir, "type"))
		if err != nil {
			continue
		}
		if typ != "Mains" && typ != "USB" {
			continue
		}
		online, err := readTrim(filepath.Join(dir, "online"))
		if err != nil {
			continue
		}
		if online == "1" {
			return true, nil
		}
	}
	return false, nil
}

// Battery finds the battery to manage: BAT0 when present, else the first
// supply of type Battery. It returns nil, nil on machines without a
// battery, where only profile handling applies.
func (s Supply) Battery() (*Battery, error) {
	if b, ok := s.batteryNamed("BAT0"); ok {
		return b, nil
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.Dir, err)
	}
	for _, entry := range entries {
		if b, ok := s.batteryNamed(entry.Name()); ok {
			return b, nil
		}
	}
	return nil, nil
}

// batteryNamed reports whether name is a battery supply. A missing or
// unreadable type file just means "not a battery".
func (s Supply) batteryNamed(name string) (*Battery, bool) {
	dir := filepath.Join(s.Dir, name)
	typ, err := readTrim(filepath.Join(dir, "type"))
	if err != nil || typ != "Battery" {
		return nil, false
	}
	return &Battery{Name: name, Path: dir}, true
}

// Thresholds is a battery's charge_control start/end pair, in percent of
// full capacity.
type Thresholds struct {
	Start, End int
}

// Thresholds reads the current charge_control start and end thresholds.
func (b Battery) Thresholds() (Thresholds, error) {
	start, err := b.threshold(startAttr)
	if err != nil {
		return Thresholds{}, err
	}
	end, err := b.threshold(endAttr)
	if err != nil {
		return Thresholds{}, err
	}
	return Thresholds{Start: start, End: end}, nil
}

// Writable reports whether the charge thresholds can be written, which is
// false on desktops and batteries whose EC does not expose them.
func (b Battery) Writable() bool {
	f, err := os.OpenFile(filepath.Join(b.Path, endAttr), os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	return f.Close() == nil
}

// SetThresholds moves both charge thresholds to want and reports whether
// anything was written. The embedded controller rejects a start above the
// current end, so the end is written first when raising and the start
// first when lowering; values already in place are never rewritten.
func (b Battery) SetThresholds(want Thresholds) (bool, error) {
	cur, err := b.Thresholds()
	if err != nil {
		return false, fmt.Errorf("reading %s thresholds: %w", b.Name, err)
	}
	if cur == want {
		return false, nil
	}
	for _, attr := range Order(cur.End, want.End) {
		value := want.Start
		if attr == endAttr {
			value = want.End
		}
		path := filepath.Join(b.Path, attr)
		if err := os.WriteFile(path, []byte(strconv.Itoa(value)), 0o600); err != nil {
			return false, fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return true, nil
}

// Order returns the two charge_control attributes in the safe write order
// for moving from curEnd to end.
func Order(curEnd, end int) [2]string {
	if end > curEnd {
		return [2]string{endAttr, startAttr}
	}
	return [2]string{startAttr, endAttr}
}

func (b Battery) threshold(attr string) (int, error) {
	raw, err := readTrim(filepath.Join(b.Path, attr))
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", attr, err)
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parsing %s %q: %w", attr, raw, err)
	}
	return value, nil
}

func readTrim(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}
