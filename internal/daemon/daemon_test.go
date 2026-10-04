package daemon_test

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/adaptive-power-manager/internal/config"
	"github.com/stubbedev/adaptive-power-manager/internal/daemon"
)

// fakeProfiles records Set calls and answers Available from a fixed list.
type fakeProfiles struct {
	available []string
	sets      []string
	fail      bool
}

func (f *fakeProfiles) Available() ([]string, error) {
	if f.fail {
		return nil, errors.New("dbus down")
	}
	return f.available, nil
}

func (f *fakeProfiles) Set(name string) error {
	if f.fail {
		return errors.New("dbus down")
	}
	f.sets = append(f.sets, name)
	return nil
}

// fixture redirects every path into a temp dir and builds a battery whose
// charge thresholds live in plain files, exactly like the sysfs the bash
// policy was tested against.
type fixture struct {
	root     string
	profiles *fakeProfiles
	log      *slog.Logger
	cfg      config.Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	for dir, env := range map[string]string{
		"ps":    "APM_SUPPLY_DIR",
		"state": "APM_STATE_DIR",
		"run":   "APM_RUNTIME_DIR",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(env, filepath.Join(root, dir))
	}
	f := &fixture{
		root:     root,
		profiles: &fakeProfiles{available: []string{"performance", "balanced", "power-saver"}},
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg:      config.Load(),
	}
	f.plug(t)
	f.writeBattery(t, "75", "80")
	return f
}

func (f *fixture) writeBattery(t *testing.T, start, end string) {
	t.Helper()
	bat := filepath.Join(f.root, "ps", "BAT0")
	if err := os.MkdirAll(bat, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"type":                           "Battery",
		"charge_control_start_threshold": start,
		"charge_control_end_threshold":   end,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(bat, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fixture) thresholds(t *testing.T) string {
	t.Helper()
	bat := filepath.Join(f.root, "ps", "BAT0")
	start, err := os.ReadFile(filepath.Join(bat, "charge_control_start_threshold"))
	if err != nil {
		t.Fatal(err)
	}
	end, err := os.ReadFile(filepath.Join(bat, "charge_control_end_threshold"))
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s/%s", start, end)
}

func (f *fixture) plug(t *testing.T) {
	t.Helper()
	f.setOnline(t, "1")
}

func (f *fixture) unplug(t *testing.T) {
	t.Helper()
	f.setOnline(t, "0")
}

func (f *fixture) setOnline(t *testing.T, value string) {
	t.Helper()
	online := filepath.Join(f.root, "ps", "AC", "online")
	if err := os.MkdirAll(filepath.Dir(online), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(online, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "ps", "AC", "type"), []byte("Mains"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// eval runs one evaluation at a fixed wall clock. Monday 2026-01-05 16:00
// is ISO weekday 1, minute 960.
func (f *fixture) eval(t *testing.T, dow, hour, minute int) daemon.Result {
	t.Helper()
	// 2026-01-05 is a Monday; the offset picks the requested weekday.
	base := time.Date(2026, 1, 5, hour, minute, 0, 0, time.UTC)
	now := base.AddDate(0, 0, dow-1)
	result, err := daemon.Eval(f.cfg, f.log, now, f.profiles)
	if err != nil {
		t.Fatalf("Eval() error = %v", err)
	}
	return result
}

func (f *fixture) seedHistory(t *testing.T, lines []string) {
	t.Helper()
	path := f.cfg.HistoryPath()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFirstRunHoldsBaseAndPicksProfile(t *testing.T) {
	f := newFixture(t)

	f.eval(t, 1, 16, 0)

	if got := f.thresholds(t); got != "75/80" {
		t.Errorf("thresholds = %s, want 75/80 with no history", got)
	}
	if len(f.profiles.sets) != 1 || f.profiles.sets[0] != "performance" {
		t.Errorf("sets = %v, want exactly one performance on the first run", f.profiles.sets)
	}
}

func TestTimerDoesNotRestompProfile(t *testing.T) {
	f := newFixture(t)

	f.eval(t, 1, 16, 0)
	f.eval(t, 1, 16, 5)

	if len(f.profiles.sets) != 1 {
		t.Errorf("sets = %v, want a hand-picked profile to survive edge-less runs", f.profiles.sets)
	}
}

func TestAdaptiveThresholds(t *testing.T) {
	f := newFixture(t)
	f.eval(t, 1, 16, 0)
	f.seedHistory(t, []string{"1 1020", "1 1035", "1 1005", "1 1050"})

	tests := []struct {
		name string
		dow  int
		hour int
		min  int
		want string
	}{
		{"an hour before the usual unplug, top up", 1, 16, 0, "95/100"},
		{"seven hours before it, hold and come back down", 1, 10, 0, "75/80"},
		{"after the last unplug of the day, hold overnight", 1, 23, 0, "75/80"},
		{"no same-day history falls back to every weekday", 3, 16, 0, "95/100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.eval(t, tt.dow, tt.hour, tt.min)
			if got := f.thresholds(t); got != tt.want {
				t.Errorf("thresholds = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestFullNowOverridesAndClearsOnUnplug(t *testing.T) {
	f := newFixture(t)
	f.eval(t, 1, 16, 0)

	if err := os.WriteFile(f.cfg.FullNowPath(), []byte("now"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.eval(t, 1, 10, 0)
	if got := f.thresholds(t); got != "95/100" {
		t.Errorf("thresholds = %s, want 95/100 with a pending full-now request", got)
	}

	f.writeBattery(t, "95", "100")
	f.unplug(t)
	f.eval(t, 2, 17, 10)
	if got := f.thresholds(t); got != "95/100" {
		t.Errorf("thresholds = %s, want untouched while on battery", got)
	}
	if _, err := os.Stat(f.cfg.FullNowPath()); !os.IsNotExist(err) {
		t.Error("full-now flag survived the unplug, want removed")
	}
	entries, err := os.ReadFile(f.cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	// Tuesday 17:10 is minute 1030: the unplug is recorded against the
	// right weekday and minute.
	if last := entries[len(entries)-7:]; string(last) != "2 1030\n" {
		t.Errorf("last history entry = %q, want %q", last, "2 1030\n")
	}
}

func TestUnplugDropsToPowerSaver(t *testing.T) {
	f := newFixture(t)
	f.eval(t, 1, 16, 0)
	f.unplug(t)
	f.eval(t, 2, 17, 10)

	if len(f.profiles.sets) < 2 || f.profiles.sets[len(f.profiles.sets)-1] != "power-saver" {
		t.Errorf("sets = %v, want the unplug to drop to power-saver", f.profiles.sets)
	}
}

func TestUnwritableBatteryIsSkipped(t *testing.T) {
	f := newFixture(t)
	// No charge threshold files at all: a desktop or an EC without the
	// attributes. Only the profile may be touched.
	bat := filepath.Join(f.root, "ps", "BAT0")
	if err := os.MkdirAll(bat, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bat, "type"), []byte("Battery"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := daemon.Eval(f.cfg, f.log, time.Date(2026, 1, 5, 16, 0, 0, 0, time.UTC), f.profiles); err != nil {
		t.Fatalf("Eval() error = %v, want a graceful skip", err)
	}
}

func TestProfileFailureIsNotFatal(t *testing.T) {
	f := newFixture(t)
	f.profiles.fail = true

	result, err := daemon.Eval(f.cfg, f.log, time.Date(2026, 1, 5, 16, 0, 0, 0, time.UTC), f.profiles)
	if err != nil {
		t.Fatalf("Eval() error = %v, want thresholds to still apply", err)
	}
	if !result.OnAC {
		t.Error("OnAC = false, want true")
	}
}

func TestNoProfilesClientStillManagesThresholds(t *testing.T) {
	f := newFixture(t)
	f.seedHistory(t, []string{"1 1020", "1 1035", "1 1005", "1 1050"})

	if _, err := daemon.Eval(f.cfg, f.log, time.Date(2026, 1, 5, 16, 0, 0, 0, time.UTC), nil); err != nil {
		t.Fatalf("Eval() error = %v", err)
	}
	if got := f.thresholds(t); got != "95/100" {
		t.Errorf("thresholds = %s, want 95/100 without ppd", got)
	}
}
