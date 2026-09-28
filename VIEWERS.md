# TUI and GUI viewers: design notes

This document accompanies the `feature/tui-gui-viewers` branch. It explains
what changed, why, how it was verified, and what is still open, so the
change can be reviewed without reconstructing the reasoning from the diff.

## Goals

- A lightweight way to view TopsWatch data without a browser, on the
  device itself (handhelds, developer systems running Ubuntu Desktop) and
  over a remote shell.
- Keep the monitoring tool's own CPU overhead small and configurable.
- Do not change existing behaviour: web UI, Prometheus output, `--text`,
  and the JPEG snapshot must be unaffected.

## Architecture

Both viewers are **clients of a running daemon**. They use the existing
HTTP endpoints (`/api/devices`, `/api/metrics/latest`,
`/api/metrics/history`, `/api/metrics/stream`, `/snapshot.jpg`) and never
touch sysfs, perf, or PMT. Consequences:

- The daemon collects once, however many viewers attach.
- Viewers need no root or perf permissions and no config file.
- Viewers default to `localhost:9876` and accept `--connect` for a remote
  daemon. IPv6 literals work (`[fe80::1%eth0]:9876`).

```
 sysfs / perf / PMT ──> daemon (collector, history, web) ──┬── browser (app.js)
                                                          ├── Prometheus / Grafana
                                                          ├── topswatch --tui   (SSE or poll)
                                                          └── topswatch-gui    (/snapshot.jpg)
```

### Packages added

| Package | Purpose |
|---|---|
| `internal/metricdef` | Shared presentation table for headline metrics (see migration below). |
| `internal/client` | HTTP/SSE client for the daemon. Address normalization, JSON endpoints, snapshot fetch, SSE stream with cancellation. |
| `internal/tui` | Bubble Tea terminal dashboard. Pure rendering functions over a small series store, thin event model. |
| `internal/gui` | Fyne window showing `/snapshot.jpg` on a timer. Build tag `gui`. |
| `internal/testfixture` | Deterministic samples/devices for tests. |
| `cmd/topswatch-gui` | GUI entry point (build tag `gui`). |

### Dependencies added

TUI (pure Go, in the daemon binary): `charmbracelet/bubbletea`,
`charmbracelet/lipgloss`, `NimbleMarkets/ntcharts`. The daemon binary is
still `CGO_ENABLED=0` and static; the Docker build is unchanged.

GUI (only with `-tags gui`): `fyne.io/fyne/v2`. Fyne needs cgo and
OpenGL/X11 headers, so `go build ./...`, `go vet ./...`, `go test ./...`
and the Dockerfile do **not** compile it. The GUI is built with `make gui`.

## Migration of metric definitions

Before this change the presentation of each headline metric (key, label,
unit, colour, precision, fixed axis max, aggregate flag, transform, and
which metrics appear on the module chart) was defined twice:

- `internal/web/static/app.js` (`cpuMetricDefs`, `npuMetricDefs`,
  `gpuMetricDefs`, `*ChartSeries`)
- `internal/web/snapshot.go` (`snapMetric`, `snapDefs`, `snapModuleOrder`,
  plus a hard-coded exclusion list in `drawChart`)

A TUI and GUI would have been a third and fourth copy. The Go copy was
moved to `internal/metricdef` so every Go consumer reads one table.

**What moved, field by field** (`snapshot.go` → `metricdef.Def`):

| Old (`snapMetric`) | New (`metricdef.Def`) | Notes |
|---|---|---|
| `key` | `Key` | unchanged |
| `label` (e.g. `"UTIL"`) | `Short` | `Label` added with the long form used by app.js (`"Utilization"`) |
| `unit` | `Unit` | unchanged |
| `color color.RGBA` | `Color string` (`"#rrggbb"`) | same values as `style.css`; snapshot converts with `hexColor` |
| `prec` | `Precision` | unchanged |
| `max` | `Max` | unchanged (0 = autoscale) |
| `aggregate` | `Aggregate` | unchanged |
| `transform` | `Transform` | unchanged |
| exclusion list in `drawChart` | `Chart bool` | `ddr_bandwidth` and `memory_used` are card-only, as before |
| `snapModuleOrder` | `metricdef.Order` | `cpu, npu, gpu` |
| `sampleMetricValue` logic | `metricdef.Raw` / `metricdef.Value` | identical semantics; snapshot's helper now delegates |

**What did not change:** `app.js` still has its own copy and the web UI
is untouched. Folding it in (for example by serving the table from a
`/api/definitions` endpoint) is a possible follow-up, but it changes the
browser dashboard and is out of scope here.

**Proof the snapshot is unchanged:** `internal/web/snapshot_test.go`
renders a fixture history to a PNG and compares it pixel-for-pixel with
`internal/web/testdata/snapshot_golden.png`. The golden was generated
*before* the refactor (with the original `snapDefs`) and the test passes
unchanged after it. The only other edit to `snapshot.go` is an injectable
clock (`snapNow`) so the header timestamp is deterministic in the test.

## Tests added

The repository had no tests. The baseline tests were written first so the
refactor could be checked against them:

| Test | Guards |
|---|---|
| `internal/web/snapshot_test.go` | JPEG dashboard is pixel-identical (golden PNG); empty history does not panic |
| `internal/web/prometheus_test.go` | exported Prometheus metric names (Grafana contract) |
| `internal/textout/textout_test.go` | `--text` output (golden text); byte formatting |
| `internal/collector/history_test.go` | tier bucketing at 1s and 5s intervals, labelled series stay separate, unknown range falls back |
| `internal/metricdef` | value lookup, aggregation, transform, table completeness |
| `internal/client` | address normalization incl. IPv6, every endpoint against a fake daemon, SSE parsing, cancellation |
| `internal/tui` | store, sparklines, layout at several terminal sizes (no line overflows), frame rendering, chart cache, daemon-unreachable recovery |

Run with `make test`. Regenerate goldens deliberately with
`go test ./internal/web ./internal/textout -update` and review the diff.

## Verification on hardware (Intel Core Ultra 5 335, Ubuntu 24.04, kernel 6.18)

- Daemon built with the refactor runs with all sources live (RAPL, PMT,
  perf engine counters, Xe freq); `/api/*`, `/metrics` (52 series) and
  `/snapshot.jpg` served as before.
- TUI over SSH in a 120x40, 100x30, and 80x24 terminal; stream mode and
  `--refresh 2s`/`5s`; `--connect [::1]:9876`; unreachable host shows the
  error and keeps retrying; invalid address exits with a message;
  `r`/`p`/`q` behave.
- GUI launched on the device's GNOME (X11) session, window renders the
  live dashboard.
- Docker: the image builds from this branch with the unchanged
  Dockerfile (the GUI is excluded by its build tag, so no cgo). Inside
  the running container `docker exec -it -e TERM=xterm-256color <name>
  /topswatch --tui` renders the dashboard over the container loopback,
  and a host-side `topswatch --tui --connect localhost:<published port>`
  attaches through the published port.

### Measured overhead (single core, 60 s windows, `utime+stime` from `/proc`)

Daemon, no viewer attached:

| `--interval` | CPU | RSS |
|---|---|---|
| 1s (current default) | 2.6% | 18 MB |
| 2s | 1.2% | 17 MB |
| 5s | 0.5% | 16 MB |

Cost is linear in the collection rate: roughly 25 ms of CPU per tick,
dominated by the `/proc` walk and sysfs/perf reads.

Viewers, attached to a 1s daemon:

| Viewer | CPU | RSS |
|---|---|---|
| TUI, stream (redraw per sample) | 1.3% | 20 MB |
| TUI, `--refresh 5s` | 0.85% | 20 MB |
| GUI, `--refresh 1s` | 1.7% (+2.5% in the daemon for JPEG rendering) | 150 MB |
| GUI, `--refresh 5s` | 2.0% (+0.6% in the daemon) | 150 MB |

TUI and GUI figures are from a daemon whose 5-minute ring was full (300
samples), i.e. worst case for chart drawing. The GUI's own cost barely
depends on the refresh rate; it is mostly the toolkit's event loop.

The first TUI build measured 4.5% because it redrew every second on a
clock tick and Bubble Tea ran its renderer at 60 fps. The clock tick was
removed, module charts are memoized until their data changes, and the
frame rate is capped at 10. The GUI's cost is JPEG decode plus a GL
redraw; the daemon additionally renders one JPEG per fetch.

### Viewer comparison including the browser (same device, GNOME on X11)

Each scenario: daemon at 1s, viewer attached for 60 s after a 15 s settle.
"system" is all busy CPU on the machine as % of one core; the other
columns are per-process (Firefox is its whole cgroup). Two baselines were
taken, first and last, to check drift.

| Scenario | system | daemon | viewer | Xorg | gnome-shell | terminal |
|---|---|---|---|---|---|---|
| baseline (daemon only) | 47–49 | 2.5–2.8 | 0 | 0.1 | 0.1 | 0 |
| TUI, stream, headless (tmux / SSH) | 48.9 | 2.5 | 1.3 | 0 | 0.1 | 0 |
| TUI, `--refresh 5s`, headless | 48.4 | 2.8 | 0.9 | 0 | 0 | 0 |
| TUI, stream, in gnome-terminal on the desktop | 49.9 | 2.7 | 1.2 | 0.5 | 0.2 | 0.6 |
| GUI, 1s refresh | 52.8 | 5.3 | 2.9 | 0.3 | 0.3 | 0 |
| GUI, 5s refresh | 48.9 | 3.2 | 2.0 | – | – | 0 |
| Firefox, web UI, fresh profile, 1000x900 | 87.8 | 2.9 | 17.6 | 14.3 | 5.3 | 0 |

Added cost over baseline, roughly: TUI over SSH **+2**, TUI in a desktop
terminal **+2.5**, GUI **+4.5** at 1s (about half of it in the daemon,
which renders a JPEG per fetch) or **+2.5** at 5s, browser **+40**.

Caveats: one 60 s window per scenario, and the baseline itself moved by
about ±2 between runs, so differences smaller than that are noise. All
rows are steady-state (300-sample ring) and the desktop rows were
captured with the screen unlocked and the monitor on. An earlier pass
with the screen locked gave the same Firefox and GUI figures but showed
gnome-shell at ~10% for the gnome-terminal case; that was a one-off from
the terminal window being created during the sample window, not a
steady-state cost. The ordering is not in doubt.

## Open question: default intervals

Two independent knobs exist:

1. **Daemon `collector.interval`** (config / `--interval`), default 1s.
   This is where the CPU goes. Moving the default to 2s halves the
   daemon's overhead; the history tiers, snapshot and TUI all handle
   sparser samples (covered by `history_test.go`). The 5-minute tier
   simply holds fewer points. Anything that assumes 1 Hz (Grafana panel
   step, Prometheus scrape interval) should be checked before changing it.
2. **Viewer refresh.** TUI defaults to the daemon's stream (one knob to
   reason about); `--refresh N` polls instead. GUI defaults to 1s.

No default was changed in this branch. Recommendation: keep the daemon at
1s unless the ~2.6% matters on the target device, and prefer lowering
viewer refresh first since that is free of side effects.

## Not done / follow-ups

- Per-core and per-engine panels and the process table are web-only for
  now; the TUI shows headline metrics.
- The GUI is deliberately minimal (image viewer). A native Fyne layout
  driven by `metricdef` would allow interaction, at the cost of a second
  renderer.
- `app.js` still carries its own copy of the metric definitions.
