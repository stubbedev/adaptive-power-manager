package daemon

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestTouchesOnBattery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body []any
		want bool
	}{
		{"changed", []any{"org.freedesktop.UPower", map[string]dbus.Variant{"OnBattery": dbus.MakeVariant(true)}, []string{}}, true},
		{"invalidated", []any{"org.freedesktop.UPower", map[string]dbus.Variant{}, []string{"OnBattery"}}, true},
		{"lid only", []any{"org.freedesktop.UPower", map[string]dbus.Variant{"LidIsClosed": dbus.MakeVariant(true)}, []string{}}, false},
		{"malformed", []any{"org.freedesktop.UPower"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := touchesOnBattery(&dbus.Signal{Body: tt.body}); got != tt.want {
				t.Errorf("touchesOnBattery() = %v, want %v", got, tt.want)
			}
		})
	}
}

func expectWake(t *testing.T, c <-chan struct{}, within time.Duration) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(within):
		t.Fatalf("no wakeup within %v", within)
	}
}

func expectQuiet(t *testing.T, c <-chan struct{}, during time.Duration) {
	t.Helper()
	select {
	case <-c:
		t.Fatal("unexpected wakeup")
	case <-time.After(during):
	}
}

func TestWallTimer(t *testing.T) {
	t.Parallel()
	timer, err := newWallTimer()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = timer.Close() }()

	if err := timer.Arm(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	expectWake(t, timer.wakes(), 2*time.Second)

	if err := timer.Arm(time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	expectWake(t, timer.wakes(), 2*time.Second)

	if err := timer.Arm(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := timer.Arm(time.Time{}); err != nil {
		t.Fatal(err)
	}
	expectQuiet(t, timer.wakes(), 200*time.Millisecond)
}

func TestWatchDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	watch, err := watchDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = watch.Close() }()

	flag := filepath.Join(dir, "full-now")
	if err := os.WriteFile(flag, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectWake(t, watch.wakes(), 2*time.Second)

	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	expectWake(t, watch.wakes(), 2*time.Second)
}

func TestWatchDirMissing(t *testing.T) {
	t.Parallel()
	if _, err := watchDir(filepath.Join(t.TempDir(), "absent")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("watchDir() error = %v, want not-exist", err)
	}
}

func TestNilWatchNeverWakes(t *testing.T) {
	t.Parallel()
	var watch *fdWake
	if watch.wakes() != nil {
		t.Error("nil watch returned a channel, want nil so select never fires")
	}
}
