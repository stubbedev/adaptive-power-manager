// Package profiles talks to power-profiles-daemon over the system bus, and
// decides which profile belongs on which power source.
package profiles

import (
	"errors"
	"fmt"
	"slices"

	"github.com/godbus/dbus/v5"
)

// ErrAbsent reports that power-profiles-daemon is not on the system bus,
// which callers treat as "profile handling unavailable", not as a fault.
var ErrAbsent = errors.New("power-profiles-daemon not found on the system bus")

type endpoint struct {
	bus   string
	path  dbus.ObjectPath
	iface string
}

// Bus names and object paths, modern first and legacy second: ppd moved to
// the org.freedesktop.UPower prefix in 0.10 but distros shipped the old
// net.hadess name for years after.
var known = [...]endpoint{
	{"org.freedesktop.UPower.PowerProfiles", "/net/hadess/PowerProfiles", "org.freedesktop.UPower.PowerProfiles"},
	{"net.hadess.PowerProfiles", "/net/hadess/PowerProfiles", "net.hadess.PowerProfiles"},
}

// Client talks to power-profiles-daemon over a caller-owned bus
// connection. The daemon is located on first use and again after it
// disappears, so a ppd that starts late or restarts is picked up without
// reconnecting.
type Client struct {
	conn  *dbus.Conn
	found *endpoint
}

// New returns a client on conn; it does no I/O until first used.
func New(conn *dbus.Conn) *Client {
	return &Client{conn: conn}
}

// Available lists the profiles ppd offers.
func (c *Client) Available() ([]string, error) {
	variant, err := c.property("Profiles")
	if err != nil {
		return nil, err
	}
	raw, ok := variant.Value().([]map[string]dbus.Variant)
	if !ok {
		return nil, fmt.Errorf("unexpected Profiles type %T", variant.Value())
	}
	names := make([]string, 0, len(raw))
	for _, profile := range raw {
		inner, ok := profile["Profile"]
		if !ok {
			continue
		}
		if name, ok := inner.Value().(string); ok {
			names = append(names, name)
		}
	}
	return names, nil
}

// Active returns the name of the active profile.
func (c *Client) Active() (string, error) {
	variant, err := c.property("ActiveProfile")
	if err != nil {
		return "", err
	}
	name, ok := variant.Value().(string)
	if !ok {
		return "", fmt.Errorf("unexpected ActiveProfile type %T", variant.Value())
	}
	return name, nil
}

// Set activates the named profile.
func (c *Client) Set(name string) error {
	ep, err := c.locate()
	if err != nil {
		return err
	}
	// SetProperty passes the value through as-is, so the variant wrapping
	// that the org.freedesktop.DBus.Properties.Set signature expects is on
	// us; a raw string makes ppd reject the whole call.
	if err := c.object(ep).SetProperty(ep.iface+".ActiveProfile", dbus.MakeVariant(name)); err != nil {
		c.found = nil
		return fmt.Errorf("setting ActiveProfile to %q on %s: %w", name, ep.bus, err)
	}
	return nil
}

// property reads one ppd property, forgetting the located endpoint when
// the read fails so the next call searches again.
func (c *Client) property(name string) (dbus.Variant, error) {
	ep, err := c.locate()
	if err != nil {
		return dbus.Variant{}, err
	}
	variant, err := c.object(ep).GetProperty(ep.iface + "." + name)
	if err != nil {
		c.found = nil
		return dbus.Variant{}, fmt.Errorf("reading %s from %s: %w", name, ep.bus, err)
	}
	return variant, nil
}

// locate returns the endpoint ppd answers on, probing the known names when
// none is remembered.
func (c *Client) locate() (endpoint, error) {
	if c.found != nil {
		return *c.found, nil
	}
	errs := make([]error, 0, len(known))
	for _, ep := range known {
		if _, err := c.object(ep).GetProperty(ep.iface + ".ActiveProfile"); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ep.bus, err))
			continue
		}
		c.found = &ep
		return ep, nil
	}
	return endpoint{}, fmt.Errorf("%w: %w", ErrAbsent, errors.Join(errs...))
}

func (c *Client) object(ep endpoint) dbus.BusObject {
	return c.conn.Object(ep.bus, ep.path)
}

// Pick returns the profile to activate for the given power source: full
// performance on AC, power-saver on battery, each falling back to balanced
// when unavailable. An empty available set yields "", meaning "nothing to
// do"; ppd always offers balanced, so that case only means ppd is absent.
func Pick(available []string, onAC bool) string {
	if len(available) == 0 {
		return ""
	}
	want := "balanced"
	switch {
	case onAC && slices.Contains(available, "performance"):
		want = "performance"
	case !onAC && slices.Contains(available, "power-saver"):
		want = "power-saver"
	}
	return want
}
