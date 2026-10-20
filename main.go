// Command adaptive-power-manager applies power-source policy on Linux
// laptops: it swaps the power-profiles-daemon profile on AC edges and
// adaptively moves the battery charge thresholds so the machine leaves the
// desk with a full battery without spending its life plugged in at 100%.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/stubbedev/adaptive-power-manager/internal/config"
	"github.com/stubbedev/adaptive-power-manager/internal/daemon"
	"github.com/stubbedev/adaptive-power-manager/internal/history"
	"github.com/stubbedev/adaptive-power-manager/internal/predict"
	"github.com/stubbedev/adaptive-power-manager/internal/profiles"
	"github.com/stubbedev/adaptive-power-manager/internal/supply"
)

// version is overridden at build time via -ldflags -X.
var version = "dev"

const usage = `adaptive-power-manager: power profile swapping and adaptive battery charging

Usage:
  adaptive-power-manager run       Evaluate continuously (the daemon mode)
  adaptive-power-manager once      Evaluate once (for timer/udev-driven setups)
  adaptive-power-manager full      Request a charge to 100% until the next unplug
  adaptive-power-manager status    Print the current state and prediction
  adaptive-power-manager predict   Print the predicted unplug time
  adaptive-power-manager version   Print the version

Paths can be overridden with APM_SUPPLY_DIR, APM_STATE_DIR and APM_RUNTIME_DIR.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := config.Load()

	err := dispatch(os.Args[1], cfg, log)
	if err != nil {
		if errors.Is(err, errUnknownCommand) {
			fmt.Fprint(os.Stderr, err)
			os.Exit(2)
		}
		log.Error("adaptive-power-manager failed", "err", err)
		os.Exit(1)
	}
}

// errUnknownCommand marks dispatch failures that are usage problems and
// exit non-zero after printing the rendered message.
var errUnknownCommand = errors.New("unknown command")

func dispatch(command string, cfg config.Config, log *slog.Logger) error {
	switch command {
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return daemon.Run(ctx, cfg, log)
	case "once":
		return withProfiles(log, func(client *profiles.Client) error {
			var pc daemon.ProfileClient
			if client != nil {
				pc = client
			}
			result, err := daemon.Eval(cfg, log, time.Now(), pc)
			log.Info("evaluated", "result", result)
			return err
		})
	case "full":
		return requestFull(cfg)
	case "status":
		return withProfiles(log, func(client *profiles.Client) error {
			out, err := status(cfg, time.Now(), client)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		})
	case "predict":
		out, err := showPrediction(cfg, time.Now())
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	case "version":
		fmt.Println("adaptive-power-manager " + version)
		return nil
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("%w: %q\n\n%s", errUnknownCommand, command, usage)
	}
}

// withProfiles runs fn with a power-profiles-daemon client on a fresh
// system bus connection, or with nil when there is no bus: one-shot
// commands must not fail on a machine without D-Bus.
func withProfiles(log *slog.Logger, fn func(*profiles.Client) error) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		log.Info("system bus unavailable, skipping power profiles", "err", err)
		return fn(nil)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Warn("closing system bus", "err", err)
		}
	}()
	return fn(profiles.New(conn))
}

func requestFull(cfg config.Config) error {
	path := cfg.FullNowPath()
	err := os.WriteFile(path, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s is missing, is adaptive-power-manager.service running?", cfg.RuntimeDir)
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	fmt.Println("Charging to 100%. The cap comes back when you unplug.")
	return nil
}

// status renders the current state. The output accumulates through
// fmt.Appendf, which cannot fail, so the caller gets one string to print.
func status(cfg config.Config, now time.Time, client *profiles.Client) (string, error) {
	sup := supply.Supply{Dir: cfg.SupplyDir}

	var out []byte
	onAC, err := sup.OnAC()
	if err != nil {
		return "", err
	}
	state := "on battery"
	if onAC {
		state = "connected"
	}
	out = fmt.Appendf(out, "AC:          %s\n", state)

	battery, err := sup.Battery()
	if err != nil {
		return "", err
	}
	if battery != nil {
		if thresholds, err := battery.Thresholds(); err == nil {
			out = fmt.Appendf(out, "battery:     %s (%d-%d%%)\n", battery.Name, thresholds.Start, thresholds.End)
		} else {
			out = fmt.Appendf(out, "battery:     %s (thresholds unavailable: %v)\n", battery.Name, err)
		}
	}

	out = appendProfile(out, client)

	_, err = os.Lstat(cfg.FullNowPath())
	out = fmt.Appendf(out, "full-now:    %v\n", err == nil)

	entries, err := history.Load(cfg.HistoryPath())
	if err != nil {
		return "", err
	}
	out = fmt.Appendf(out, "history:     %d unplugs\n", len(entries))

	at := history.At(now)
	if predicted, ok := predict.Next(entries, at, config.MinSamples); ok {
		out = fmt.Appendf(out, "prediction:  %s at %02d:%02d, top-up window opens %d min before\n",
			weekday(at.Dow), predicted/60, predicted%60, config.TopupWindowMin)
	} else {
		out = fmt.Appendf(out, "prediction:  none yet, holding the base thresholds\n")
	}
	return string(out), nil
}

func appendProfile(out []byte, client *profiles.Client) []byte {
	if client == nil {
		return fmt.Appendf(out, "profile:     system bus not reachable\n")
	}
	active, err := client.Active()
	if err != nil {
		return fmt.Appendf(out, "profile:     %v\n", err)
	}
	available, err := client.Available()
	if err != nil {
		return fmt.Appendf(out, "profile:     %s (available: %v)\n", active, err)
	}
	return fmt.Appendf(out, "profile:     %s (available: %v)\n", active, available)
}

func showPrediction(cfg config.Config, now time.Time) (string, error) {
	entries, err := history.Load(cfg.HistoryPath())
	if err != nil {
		return "", err
	}
	at := history.At(now)
	predicted, ok := predict.Next(entries, at, config.MinSamples)
	if !ok {
		return fmt.Sprintf("no prediction: fewer than %d unplug records ahead of now\n", config.MinSamples), nil
	}
	return fmt.Sprintf("%s at %02d:%02d\n", weekday(at.Dow), predicted/60, predicted%60), nil
}

// weekday names an ISO weekday (1 = Monday .. 7 = Sunday).
func weekday(dow int) string {
	return time.Weekday(dow % 7).String()
}
