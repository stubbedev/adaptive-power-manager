// Package daemon runs the power manager: one idempotent, side-effecting
// evaluation of power source, profile and charge thresholds, driven by
// events rather than a polling loop.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/stubbedev/adaptive-power-manager/internal/config"
	"github.com/stubbedev/adaptive-power-manager/internal/history"
	"github.com/stubbedev/adaptive-power-manager/internal/predict"
	"github.com/stubbedev/adaptive-power-manager/internal/profiles"
	"github.com/stubbedev/adaptive-power-manager/internal/statefile"
	"github.com/stubbedev/adaptive-power-manager/internal/supply"
)

// ProfileClient is the slice of power-profiles-daemon the manager needs. A
// nil client means profile handling is unavailable and is skipped.
type ProfileClient interface {
	Available() ([]string, error)
	Set(name string) error
}

var (
	baseThresholds = supply.Thresholds{Start: config.BaseStart, End: config.BaseEnd}
	fullThresholds = supply.Thresholds{Start: config.FullStart, End: config.FullEnd}
)

// Result reports what one evaluation observed and changed.
type Result struct {
	OnAC           bool
	ProfileSet     string
	RecordedUnplug bool
	ClearedFullNow bool
	// Applied holds the thresholds written by this evaluation, nil when
	// they were already in place or not managed.
	Applied *supply.Thresholds
	// Recheck is when the outcome next changes with the passage of time
	// alone; zero means only an event can change it.
	Recheck time.Time
}

// Changed reports whether the evaluation altered any state.
func (r Result) Changed() bool {
	return r.ProfileSet != "" || r.RecordedUnplug || r.ClearedFullNow || r.Applied != nil
}

// LogValue renders the result for structured logs.
func (r Result) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.Bool("on_ac", r.OnAC),
		slog.String("profile", r.ProfileSet),
		slog.Bool("recorded_unplug", r.RecordedUnplug),
		slog.Bool("cleared_full_now", r.ClearedFullNow),
	}
	if r.Applied != nil {
		attrs = append(attrs, slog.String("thresholds", fmt.Sprintf("%d/%d", r.Applied.Start, r.Applied.End)))
	}
	if !r.Recheck.IsZero() {
		attrs = append(attrs, slog.Time("recheck", r.Recheck))
	}
	return slog.GroupValue(attrs...)
}

// acState is the power source as last recorded, for edge detection.
type acState byte

const (
	acUnknown acState = iota
	acBattery
	acMains
)

// Eval performs one evaluation of the policy. It is idempotent: nothing at
// the wanted state is touched, including a profile the user picked by hand
// between AC edges.
func Eval(cfg config.Config, log *slog.Logger, now time.Time, pc ProfileClient) (Result, error) {
	at := history.At(now)
	sup := supply.Supply{Dir: cfg.SupplyDir}

	onAC, err := sup.OnAC()
	if err != nil {
		return Result{}, fmt.Errorf("reading power supplies: %w", err)
	}
	cur := acBattery
	if onAC {
		cur = acMains
	}
	prev, err := readLastAC(cfg.LastACPath())
	if err != nil {
		return Result{}, err
	}
	result := Result{OnAC: onAC}

	if prev != cur {
		if err := writeLastAC(cfg.LastACPath(), cur); err != nil {
			return result, err
		}
		result.ProfileSet = applyProfile(log, pc, onAC)
	}

	if prev == acMains && cur == acBattery {
		if err := history.Add(cfg.HistoryPath(), at, config.HistMax); err != nil {
			return result, fmt.Errorf("recording unplug: %w", err)
		}
		result.RecordedUnplug = true
		// The charge-to-full promise is "the cap comes back when you
		// unplug": the flag goes with the edge, and the battery branch
		// below restores the base thresholds right away.
		cleared, err := removeIfExists(cfg.FullNowPath())
		if err != nil {
			return result, fmt.Errorf("clearing charge-to-full flag: %w", err)
		}
		result.ClearedFullNow = cleared
	}

	battery, err := sup.Battery()
	if err != nil {
		return result, fmt.Errorf("finding battery: %w", err)
	}
	if battery == nil || !battery.Writable() {
		return result, nil
	}

	want, recheck, err := wantedThresholds(cfg, now, at, onAC)
	if err != nil {
		return result, err
	}
	result.Recheck = recheck

	changed, err := battery.SetThresholds(want)
	if err != nil {
		return result, fmt.Errorf("setting charge thresholds: %w", err)
	}
	if changed {
		result.Applied = &want
	}
	return result, nil
}

// wantedThresholds decides the charge thresholds, and when that decision
// next changes with time alone. On battery the base thresholds hold; on AC
// the ceiling rises to full for a pending charge-to-full request or while
// the top-up window before the predicted unplug is open.
func wantedThresholds(cfg config.Config, now time.Time, at history.Entry, onAC bool) (supply.Thresholds, time.Time, error) {
	if !onAC {
		return baseThresholds, time.Time{}, nil
	}
	fullNow, err := exists(cfg.FullNowPath())
	if err != nil {
		return supply.Thresholds{}, time.Time{}, fmt.Errorf("checking charge-to-full flag: %w", err)
	}
	if fullNow {
		return fullThresholds, time.Time{}, nil
	}

	entries, err := history.Load(cfg.HistoryPath())
	if err != nil {
		return supply.Thresholds{}, time.Time{}, fmt.Errorf("reading unplug history: %w", err)
	}

	want := baseThresholds
	if predict.TopUp(entries, at, config.MinSamples, config.TopupWindowMin) {
		want = fullThresholds
	}

	// The weekday, and with it the whole prediction, turns over at
	// midnight even when nothing changes before.
	year, month, day := now.Date()
	recheck := time.Date(year, month, day+1, 0, 0, 0, 0, now.Location())
	if minute, ok := predict.NextChange(entries, at, config.MinSamples, config.TopupWindowMin); ok {
		recheck = time.Date(year, month, day, minute/60, minute%60, 0, 0, now.Location())
	}
	return want, recheck, nil
}

// applyProfile switches ppd to the profile for the power source and returns
// it, or "" when nothing was set. Profile trouble never blocks the
// threshold half of the policy.
func applyProfile(log *slog.Logger, pc ProfileClient, onAC bool) string {
	if pc == nil {
		return ""
	}
	available, err := pc.Available()
	if errors.Is(err, profiles.ErrAbsent) {
		log.Info("power profile handling unavailable", "err", err)
		return ""
	}
	if err != nil {
		log.Warn("listing power profiles", "err", err)
		return ""
	}
	want := profiles.Pick(available, onAC)
	if want == "" {
		return ""
	}
	if err := pc.Set(want); err != nil {
		log.Warn("setting power profile", "profile", want, "err", err)
		return ""
	}
	return want
}

// Run evaluates the policy until the context is canceled. It sleeps until
// something can change the outcome: an AC edge (UPower's OnBattery), a
// charge-to-full request (inotify on the runtime directory), the next
// time-based change Eval reports (a wall-clock timer that survives suspend
// and clock changes), or SIGHUP. Without UPower it falls back to checking
// the power source every Tick.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	migrateLegacyState(cfg, log)

	var pc ProfileClient
	var acEdges <-chan struct{}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		log.Warn("system bus unavailable: no power profiles, polling the power source", "err", err, "every", config.Tick)
	} else {
		defer closeLogged(log, "system bus", conn)
		pc = profiles.New(conn)
		if acEdges, err = watchOnBattery(conn); err != nil {
			return err
		}
	}

	requests, err := watchDir(cfg.RuntimeDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		log.Info("charge-to-full requests unavailable until restart", "dir", cfg.RuntimeDir)
	case err != nil:
		return err
	default:
		defer closeLogged(log, "runtime directory watch", requests)
	}

	timer, err := newWallTimer()
	if err != nil {
		return err
	}
	defer closeLogged(log, "timer", timer)

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	for {
		now := time.Now()
		result, err := Eval(cfg, log, now, pc)
		next := result.Recheck
		switch {
		case err != nil:
			log.Error("evaluating policy", "err", err)
			next = earliest(next, now.Add(config.Tick))
		case result.Changed():
			log.Info("applied", "result", result)
		}
		if acEdges == nil {
			next = earliest(next, now.Add(config.Tick))
		}
		if err := timer.Arm(next); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return nil
		case <-timer.wakes():
		case <-acEdges:
		case <-requests.wakes():
		case <-hup:
		}
	}
}

// earliest returns the earlier of a deadline that may be zero (none) and a
// fallback.
func earliest(deadline, fallback time.Time) time.Time {
	if deadline.IsZero() || fallback.Before(deadline) {
		return fallback
	}
	return deadline
}

// watchOnBattery subscribes to UPower's property changes and signals only
// those touching OnBattery, which flips on every AC edge; lid and other
// property changes are not worth an evaluation. The channel stays open
// until the connection closes.
func watchOnBattery(conn *dbus.Conn) (<-chan struct{}, error) {
	const iface = "org.freedesktop.UPower"
	if err := conn.AddMatchSignal(
		dbus.WithMatchSender(iface),
		dbus.WithMatchObjectPath("/org/freedesktop/UPower"),
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		dbus.WithMatchMember("PropertiesChanged"),
		dbus.WithMatchArg(0, iface),
	); err != nil {
		return nil, fmt.Errorf("subscribing to UPower signals: %w", err)
	}

	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)
	wake := make(chan struct{}, 1)
	go func() {
		for sig := range signals {
			if touchesOnBattery(sig) {
				notify(wake)
			}
		}
	}()
	return wake, nil
}

// touchesOnBattery reports whether a PropertiesChanged signal changes or
// invalidates OnBattery.
func touchesOnBattery(sig *dbus.Signal) bool {
	const property = "OnBattery"
	if len(sig.Body) != 3 {
		return false
	}
	if changed, ok := sig.Body[1].(map[string]dbus.Variant); ok {
		if _, ok := changed[property]; ok {
			return true
		}
	}
	invalidated, ok := sig.Body[2].([]string)
	return ok && slices.Contains(invalidated, property)
}

// migrateLegacyState adopts the unplug history of the replaced bash policy
// so the prediction does not start blind. It only runs for the default
// state directory, never for test or user overrides.
func migrateLegacyState(cfg config.Config, log *slog.Logger) {
	if cfg.StateDir != config.DefaultStateDir {
		return
	}
	if _, err := os.Lstat(cfg.HistoryPath()); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	legacy := filepath.Join(config.LegacyStateDir, "unplugs")
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		log.Warn("creating state directory for the legacy history", "dir", cfg.StateDir, "err", err)
		return
	}
	err := os.Rename(legacy, cfg.HistoryPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		log.Warn("adopting the legacy unplug history", "from", legacy, "err", err)
	default:
		log.Info("adopted the legacy unplug history", "from", legacy)
	}
}

func readLastAC(path string) (acState, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return acUnknown, nil
	}
	if err != nil {
		return acUnknown, fmt.Errorf("reading %s: %w", path, err)
	}
	switch string(raw) {
	case "0":
		return acBattery, nil
	case "1":
		return acMains, nil
	default:
		return acUnknown, nil
	}
}

func writeLastAC(path string, state acState) error {
	value := "0"
	if state == acMains {
		value = "1"
	}
	if err := statefile.Write(path, []byte(value)); err != nil {
		return fmt.Errorf("recording AC state: %w", err)
	}
	return nil
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// removeIfExists deletes path and reports whether there was anything to
// delete.
func removeIfExists(path string) (bool, error) {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

type closer interface {
	Close() error
}

func closeLogged(log *slog.Logger, what string, c closer) {
	if err := c.Close(); err != nil {
		log.Warn("closing "+what, "err", err)
	}
}
