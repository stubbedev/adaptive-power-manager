package daemon

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// fdWake turns a nonblocking file descriptor becoming readable into
// coalesced wakeups. The descriptor goes through the runtime poller, so
// Close unblocks the reader and ends it.
type fdWake struct {
	file *os.File
	c    chan struct{}
}

func newFDWake(fd int, name string, bufSize int) *fdWake {
	w := &fdWake{file: os.NewFile(uintptr(fd), name), c: make(chan struct{}, 1)}
	go func() {
		buf := make([]byte, bufSize)
		for {
			// ECANCELED is the wall timer reporting a clock change, which
			// is as much a reason to re-evaluate as an expiry.
			if _, err := w.file.Read(buf); err != nil && !errors.Is(err, unix.ECANCELED) {
				return
			}
			notify(w.c)
		}
	}()
	return w
}

// wakes returns the wakeup channel; nil (never ready) for a nil watch.
func (w *fdWake) wakes() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.c
}

func (w *fdWake) Close() error {
	return w.file.Close()
}

// watchDir wakes on every file written, moved or deleted in dir.
func watchDir(dir string) (*fdWake, error) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("creating inotify instance: %w", err)
	}
	const mask = unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_MOVED_FROM | unix.IN_DELETE
	if _, err := unix.InotifyAddWatch(fd, dir, mask); err != nil {
		return nil, errors.Join(fmt.Errorf("watching %s: %w", dir, err), unix.Close(fd))
	}
	return newFDWake(fd, "inotify:"+dir, 4096), nil
}

// wallTimer fires at absolute CLOCK_REALTIME deadlines. Unlike Go timers,
// which run on the monotonic clock, it still fires on time after a
// suspend, and it wakes early when the wall clock is set.
type wallTimer struct {
	*fdWake

	fd int
}

func newWallTimer() (*wallTimer, error) {
	fd, err := unix.TimerfdCreate(unix.CLOCK_REALTIME, unix.TFD_NONBLOCK|unix.TFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("creating timerfd: %w", err)
	}
	return &wallTimer{fdWake: newFDWake(fd, "timerfd", 8), fd: fd}, nil
}

// Arm sets the timer to fire at deadline, replacing any earlier setting; a
// zero deadline disarms it. A deadline already past fires immediately.
func (t *wallTimer) Arm(deadline time.Time) error {
	var spec unix.ItimerSpec
	if !deadline.IsZero() {
		spec.Value = unix.NsecToTimespec(deadline.UnixNano())
	}
	if err := unix.TimerfdSettime(t.fd, unix.TFD_TIMER_ABSTIME|unix.TFD_TIMER_CANCEL_ON_SET, &spec, nil); err != nil {
		return fmt.Errorf("arming timer for %v: %w", deadline, err)
	}
	return nil
}

// notify queues one wakeup, coalescing with any already pending.
func notify(c chan<- struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}
