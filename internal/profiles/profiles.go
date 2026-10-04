// Package profiles talks to power-profiles-daemon over the system bus, and
// decides which profile belongs on which power source.
package profiles

import (
	"errors"
	"fmt"
	"slices"

	"github.com/godbus/dbus/v5"
)

// Bus names and object paths, modern first and legacy second: ppd moved to
// the org.freedesktop.UPower prefix in 0.10 but distros shipped the old
// net.hadess name for years after.
var known = []struct {
	bus   string
	path  string
	iface string
}{
	{"org.freedesktop.UPower.PowerProfiles", "/net/hadess/PowerProfiles", "org.freedesktop.UPower.PowerProfiles"},
	{"net.hadess.PowerProfiles", "/net/hadess/PowerProfiles", "net.hadess.PowerProfiles"},
}

// Client is a live connection to power-profiles-daemon.
type Client struct {
	conn  *dbus.Conn
	obj   dbus.BusObject
	iface string
}

// Connect opens the system bus and locates power-profiles-daemon. It fails
// when ppd is absent, which callers should treat as "profile handling
// unavailable", not fatal.
func Connect() (*Client, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connecting to system bus: %w", err)
	}
	for _, k := range known {
		client := &Client{conn: conn, obj: conn.Object(k.bus, dbus.ObjectPath(k.path)), iface: k.iface}
		if _, err := client.Available(); err == nil {
			return client, nil
		}
	}
	if err := conn.Close(); err != nil {
		return nil, fmt.Errorf("closing system bus: %w", err)
	}
	return nil, errors.New("power-profiles-daemon not found on the system bus")
}

// Available lists the profiles ppd offers.
func (c *Client) Available() ([]string, error) {
	variant, err := c.obj.GetProperty(c.iface + ".Profiles")
	if err != nil {
		return nil, fmt.Errorf("reading Profiles: %w", err)
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
	variant, err := c.obj.GetProperty(c.iface + ".ActiveProfile")
	if err != nil {
		return "", fmt.Errorf("reading ActiveProfile: %w", err)
	}
	name, ok := variant.Value().(string)
	if !ok {
		return "", fmt.Errorf("unexpected ActiveProfile type %T", variant.Value())
	}
	return name, nil
}

// Set activates the named profile.
func (c *Client) Set(name string) error {
	// SetProperty passes the value through as-is, so the variant wrapping
	// that the org.freedesktop.DBus.Properties.Set signature expects is on
	// us; a raw string makes ppd reject the whole call.
	return c.obj.SetProperty(c.iface+".ActiveProfile", dbus.MakeVariant(name))
}

// Close releases the bus connection.
func (c *Client) Close() error {
	return c.conn.Close()
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
