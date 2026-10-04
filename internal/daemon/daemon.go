// Package daemon wires the evaluation loop and owns the decision: a
// safety tick, UPower signals and SIGHUP each trigger one idempotent
// evaluation of power source, profile and charge thresholds.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stubbedev/adaptive-power-manager/internal/config"
	"github.com/stubbedev/adaptive-power-manager/internal/history"
	"github.com/stubbedev/adaptive-power-manager/internal/predict"
	"github.com/stubbedev/adaptive-power-manager/internal/profiles"
	"github.com/stubbedev/adaptive-power-manager/internal/supply"
)

// ProfileClient is the slice of power-profiles-daemon the manager needs. A
// nil client means profile handling is unavailable and is skipped.
type ProfileClient interface {
	Available() ([]string, error)
	Set(name string) error
}

// Result reports what one evaluation observed and changed.
type Result struct {
	OnAC              bool
	ProfileSet        string
	RecordedUnplug    bool
	ClearedFullNow    bool
	ThresholdsApplied bool
	Start, End        int
}

// Eval performs one evaluation of the policy. It is idempotent: nothing at
// the wanted state stays untouched, including a profile the user picked by
// hand between AC edges.
func Eval(cfg config.Config, log *slog.Logger, now time.Time, pc ProfileClient) (Result, error) {
	dow, minute := clockFields(now)
	sup := supply.Supply{Dir: cfg.SupplyDir}

	onAC, err := sup.OnAC()
	if err != nil {
		return Result{}, fmt.Errorf("reading power supplies: %w", err)
	}

	prev := readLastAC(cfg.LastACPath())
	cur := "0"
	if onAC {
		cur = "1"
	}
	if err := writeLastAC(cfg.LastACPath(), cur); err != nil {
		return Result{}, fmt.Errorf("recording AC state: %w", err)
	}
	edge := prev != cur
	result := Result{OnAC: onAC}

	if edge && pc != nil {
		available, err := pc.Available()
		if err != nil {
			log.Warn("listing power profiles", "err", err)
		} else if want := profiles.Pick(available, onAC); want != "" {
			if err := pc.Set(want); err != nil {
				log.Warn("setting power profile", "err", err)
			} else {
				result.ProfileSet = want
			}
		}
	}

	if prev == "1" && cur == "0" {
		if err := history.Add(cfg.HistoryPath(), history.Entry{Dow: dow, Minute: minute}, config.HistMax); err != nil {
			return result, fmt.Errorf("recording unplug: %w", err)
		}
		result.RecordedUnplug = true
		if fullNowExists(cfg.FullNowPath()) {
			if err := os.Remove(cfg.FullNowPath()); err != nil {
				return result, fmt.Errorf("clearing charge-to-full flag: %w", err)
			}
			result.ClearedFullNow = true
		}
	}

	if !onAC {
		return result, nil
	}

	battery, err := sup.Battery()
	if err != nil {
		return result, fmt.Errorf("finding battery: %w", err)
	}
	if battery == nil || !battery.Writable() {
		return result, nil
	}

	curStart, curEnd, err := battery.Thresholds()
	if err != nil {
		return result, fmt.Errorf("reading charge thresholds: %w", err)
	}

	entries, err := history.Load(cfg.HistoryPath())
	if err != nil {
		return result, fmt.Errorf("reading unplug history: %w", err)
	}

	start, end := config.BaseStart, config.BaseEnd
	switch {
	case fullNowExists(cfg.FullNowPath()):
		start, end = config.FullStart, config.FullEnd
	default:
		if predicted, ok := predict.Next(entries, dow, minute, config.MinSamples); ok &&
			predicted-minute <= config.TopupWindowMin {
			start, end = config.FullStart, config.FullEnd
		}
	}

	if start == curStart && end == curEnd {
		return result, nil
	}
	if err := battery.SetThresholds(start, end); err != nil {
		return result, fmt.Errorf("setting charge thresholds: %w", err)
	}
	result.ThresholdsApplied = true
	result.Start, result.End = start, end
	return result, nil
}

// Run evaluates the policy on AC changes (UPower signals), every Tick as a
// safety net for the prediction boundaries, and on SIGHUP, until the
// context is canceled.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pc := connectProfiles(log)
	wake, err := watchUPower(ctx, log)
	if err != nil {
		return err
	}

	migrateLegacyState(cfg)

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	ticker := time.NewTicker(config.Tick)
	defer ticker.Stop()

	if _, err := Eval(cfg, log, time.Now(), pc); err != nil {
		log.Error("evaluating policy", "err", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-wake:
		case <-hup:
		}
		if _, err := Eval(cfg, log, time.Now(), pc); err != nil {
			log.Error("evaluating policy", "err", err)
		}
	}
}

func connectProfiles(log *slog.Logger) ProfileClient {
	client, err := profiles.Connect()
	if err != nil {
		log.Info("power profile handling unavailable", "err", err)
		return nil
	}
	return client
}

// watchUPower subscribes to UPower property changes on the system bus; the
// OnBattery property flips on every AC edge, making the daemon event-driven
// rather than a poller. The returned channel closes on context cancellation.
// A nil channel means "tick only".
func watchUPower(ctx context.Context, log *slog.Logger) (<-chan struct{}, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		log.Info("UPower wakeups unavailable, falling back to the tick", "err", err)
		return nil, nil
	}
	if err := conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.UPower"),
		dbus.WithMatchObjectPath("/org/freedesktop/UPower"),
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
	); err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			log.Warn("closing system bus", "err", closeErr)
		}
		return nil, fmt.Errorf("subscribing to UPower signals: %w", err)
	}

	wake := make(chan struct{}, 1)
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)
	go func() {
		defer close(wake)
		defer conn.RemoveSignal(signals)
		defer func() {
			if closeErr := conn.Close(); closeErr != nil {
				log.Warn("closing system bus", "err", closeErr)
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
	}()
	return wake, nil
}

// migrateLegacyState adopts the unplug history of the replaced bash policy
// so the prediction does not start blind. It only runs for the default
// state directory, never for test or user overrides.
func migrateLegacyState(cfg config.Config) {
	if cfg.StateDir != config.DefaultStateDir {
		return
	}
	if _, err := os.Stat(cfg.HistoryPath()); err == nil {
		return
	}
	legacy := filepath.Join(config.LegacyStateDir, "unplugs")
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return
	}
	_ = os.Rename(legacy, cfg.HistoryPath())
}

func readLastAC(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	switch string(raw) {
	case "0":
		return "0"
	case "1":
		return "1"
	default:
		return "unknown"
	}
}

func writeLastAC(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(value), 0o600)
}

func fullNowExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func clockFields(now time.Time) (dow, minute int) {
	dow = (int(now.Weekday())+6)%7 + 1
	minute = now.Hour()*60 + now.Minute()
	return dow, minute
}
