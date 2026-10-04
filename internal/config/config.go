// Package config carries every path and tunable of the power manager, with
// environment overrides so tests and exotic setups can redirect the sysfs,
// state and runtime locations without recompiling.
package config

import (
	"os"
	"path/filepath"
	"time"
)

// Default locations on a Linux laptop. RuntimeDir matches the bash policy
// this binary replaces, so an existing battery-full-style flag file keeps
// working across the switch.
const (
	DefaultSupplyDir  = "/sys/class/power_supply"
	DefaultStateDir   = "/var/lib/adaptive-power-manager"
	DefaultRuntimeDir = "/run/battery-charge"

	// LegacyStateDir is where the replaced bash policy kept its state. Its
	// unplug history is adopted on first run so the prediction does not
	// start from scratch.
	LegacyStateDir = "/var/lib/power-source"
)

// Charge thresholds in percent of full capacity. The battery holds between
// BaseStart and BaseEnd while docked, and charges to FullStart..FullEnd
// while a top-up window is open or a charge-to-full request is pending.
const (
	BaseStart = 75
	BaseEnd   = 80
	FullStart = 95
	FullEnd   = 100
)

// Behavioral tunables.
const (
	// TopupWindowMin is how many minutes before the predicted unplug the
	// ceiling rises to full.
	TopupWindowMin = 120
	// MinSamples is the fewest unplug records from which a prediction is
	// made. Below that the manager holds the base thresholds.
	MinSamples = 3
	// HistMax caps the unplug log so a stale habit ages out of the model.
	HistMax = 60
	// Tick is the daemon's re-evaluation cadence. UPower signals make AC
	// changes immediate; the tick only catches the prediction boundaries.
	Tick = time.Minute
)

// Config is the resolved configuration.
type Config struct {
	SupplyDir  string
	StateDir   string
	RuntimeDir string
}

// Load reads the configuration, applying APM_* environment overrides.
func Load() Config {
	return Config{
		SupplyDir:  env("APM_SUPPLY_DIR", DefaultSupplyDir),
		StateDir:   env("APM_STATE_DIR", DefaultStateDir),
		RuntimeDir: env("APM_RUNTIME_DIR", DefaultRuntimeDir),
	}
}

// HistoryPath is the unplug log.
func (c Config) HistoryPath() string {
	return filepath.Join(c.StateDir, "unplugs")
}

// LastACPath remembers the previous AC state for edge detection.
func (c Config) LastACPath() string {
	return filepath.Join(c.StateDir, "last-ac")
}

// FullNowPath is the charge-to-full request flag.
func (c Config) FullNowPath() string {
	return filepath.Join(c.RuntimeDir, "full-now")
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
