# adaptive-power-manager

macOS-style Optimized Battery Charging and power-profile switching for Linux
laptops, in one static Go binary (CGO_ENABLED=0, stdlib plus godbus only).

A battery kept plugged in at 100% wears out fast. So while the machine is on
AC, charging stops at **80%** (resuming below **75%**). The daemon learns at
what time the charger usually comes out, and finishes charging to **100%** in
the two hours before that moment, so you leave with a full battery that never
spent the night at full.

It also swaps the [power-profiles-daemon](https://gitlab.freedesktop.org/upower/power-profiles-daemon)
profile on AC edges: `performance` when plugged in, `power-saver` on battery,
falling back to `balanced` when either is unavailable. Switches happen only on
an actual power-source change, so a profile you pick by hand sticks between
edges. On a machine without a battery (or whose EC hides the charge
thresholds), only the profile half applies.

## Why this exists

The kernel's only interface is the static `charge_control_start_threshold` /
`charge_control_end_threshold` sysfs pair; scheduling is userspace policy.
UPower and GNOME expose static limits, TLP adds static ON_AC/ON_BAT variants,
neither learns anything. This is the missing adaptive half, built for
[wayle](https://github.com/stubbedev)-based setups and any other DE-less
desktop. It replaces an awk-heavy bash policy with the same behaviour, ported
scenario by scenario into the test suite.

## How it decides

- Every AC edge is recorded: the weekday and minute the charger came out.
- On unplug the charge-to-full request (if any) is cleared, the base
  75-80% thresholds are restored immediately, and the profile drops to
  `power-saver`; on plug-in it goes back to `performance`.
- On AC, the ceiling rises to 95-100% when either a charge-to-full request is
  pending or the predicted unplug is within 120 minutes.
- The prediction takes the lower quartile of recorded unplug minutes, same
  weekday first, every weekday as fallback, with strictly future times only:
  once the usual minute has passed it holds 80% overnight instead of chasing
  a stale prediction. Fewer than 3 samples means no prediction yet, and the
  base 75-80% simply holds.
- Threshold writes respect the EC ordering constraint (end before start when
  raising, start before end when lowering), and unchanged values are never
  rewritten.

## Usage

```
adaptive-power-manager run       Evaluate continuously (the daemon)
adaptive-power-manager once      Evaluate once (timer/udev-driven setups)
adaptive-power-manager full      Charge to 100% until the next unplug
adaptive-power-manager status    Current state and prediction
adaptive-power-manager predict   The predicted unplug time
```

`run` is event-driven: UPower property changes wake it on every AC edge, a
60-second tick covers the prediction boundaries, and SIGHUP forces an
evaluation. No polling of sysfs.

Paths can be redirected with `APM_SUPPLY_DIR`, `APM_STATE_DIR` and
`APM_RUNTIME_DIR`; state carries over from the bash predecessor at
`/var/lib/power-source` automatically.

## Install

With Nix:

```
nix run github:stubbedev/adaptive-power-manager -- status
```

Statically built release binaries for linux/amd64 and linux/arm64 are on the
releases page; a hardened unit template lives in
`packaging/systemd/adaptive-power-manager.service`. Requirements: a battery
exposing `charge_control_*_threshold` (ThinkPad, ASUS, Framework, ...),
`power-profiles-daemon` and `upower` for the event-driven halves. Anything
missing degrades gracefully to whatever remains.

## Development

```
devenv shell          # go 1.27, gopls, golangci-lint, just
just check            # lint + vet + test + build, mirrors CI exactly
just sync-flake       # recompute flake.nix vendorHash after dep changes
nix build             # static binary, runs go test via doCheck
```

CI runs the strict golangci-lint v2 config from `.golangci.yml`, `go vet`,
`go test`, a fake-sysfs smoke through the real binary, and the nix build with
an auto-maintained `vendorHash`. Dependabot PRs squash-merge themselves once
every check is green.

## License

[MIT](LICENSE)
