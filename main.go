// Command adaptive-power-manager applies power-source policy on Linux
// laptops: it swaps the power-profiles-daemon profile on AC edges and
// adaptively moves the battery charge thresholds so the machine leaves the
// desk with a full battery without spending its life plugged in at 100%.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

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
		result, err := daemon.Eval(cfg, log, time.Now(), connectProfilesForOneShot(log))
		logEval(log, result)
		return err
	case "full":
		return requestFull(cfg)
	case "status":
		out, err := status(cfg, time.Now())
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
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

func logEval(log *slog.Logger, result daemon.Result) {
	log.Info("evaluated",
		"on_ac", result.OnAC,
		"profile", result.ProfileSet,
		"recorded_unplug", result.RecordedUnplug,
		"cleared_full_now", result.ClearedFullNow,
		"thresholds", fmt.Sprintf("%d/%d", result.Start, result.End),
		"thresholds_applied", result.ThresholdsApplied,
	)
}

// connectProfilesForOneShot makes profile handling optional for one-shot
// invocations: no running ppd must not fail a timer or udev trigger.
func connectProfilesForOneShot(log *slog.Logger) daemon.ProfileClient {
	client, err := profiles.Connect()
	if err != nil {
		log.Info("power profile handling unavailable", "err", err)
		return nil
	}
	return client
}

func requestFull(cfg config.Config) error {
	if _, err := os.Stat(cfg.RuntimeDir); err != nil {
		return fmt.Errorf("%s is missing, is adaptive-power-manager.service running?", cfg.RuntimeDir)
	}
	path := cfg.FullNowPath()
	if err := os.WriteFile(path, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	fmt.Println("Charging to 100%. The cap comes back when you unplug.")
	return nil
}

// status renders the current state. The output accumulates through
// fmt.Appendf, which cannot fail, so the caller gets one string to print.
func status(cfg config.Config, now time.Time) (string, error) {
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
		if start, end, err := battery.Thresholds(); err == nil {
			out = fmt.Appendf(out, "battery:     %s (%d-%d%%)\n", battery.Name, start, end)
		}
	}

	if client, err := profiles.Connect(); err == nil {
		defer func() { _ = client.Close() }()
		if active, err := client.Active(); err == nil {
			available, _ := client.Available()
			out = fmt.Appendf(out, "profile:     %s (available: %v)\n", active, available)
		}
	} else {
		out = fmt.Appendf(out, "profile:     power-profiles-daemon not reachable\n")
	}

	out = fmt.Appendf(out, "full-now:    %v\n", flagExists(cfg.FullNowPath()))

	entries, err := history.Load(cfg.HistoryPath())
	if err != nil {
		return "", err
	}
	out = fmt.Appendf(out, "history:     %d unplugs\n", len(entries))

	dow, minute := clockFields(now)
	if predicted, ok := predict.Next(entries, dow, minute, config.MinSamples); ok {
		out = fmt.Appendf(out, "prediction:  %s at %02d:%02d, top-up window opens %d min before\n",
			weekday(dow), predicted/60, predicted%60, config.TopupWindowMin)
	} else {
		out = fmt.Appendf(out, "prediction:  none yet, holding the base thresholds\n")
	}
	return string(out), nil
}

func showPrediction(cfg config.Config, now time.Time) (string, error) {
	entries, err := history.Load(cfg.HistoryPath())
	if err != nil {
		return "", err
	}
	dow, minute := clockFields(now)
	predicted, ok := predict.Next(entries, dow, minute, config.MinSamples)
	if !ok {
		return fmt.Sprintf("no prediction: fewer than %d unplug records ahead of now\n", config.MinSamples), nil
	}
	return fmt.Sprintf("%s at %02d:%02d\n", weekday(dow), predicted/60, predicted%60), nil
}

func flagExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func clockFields(now time.Time) (dow, minute int) {
	dow = (int(now.Weekday())+6)%7 + 1
	minute = now.Hour()*60 + now.Minute()
	return dow, minute
}

func weekday(dow int) string {
	names := []string{"", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}
	return names[dow]
}
