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
Fyne's GLFW compiles both the X11 and Wayland backends by default on
Linux, so the build also needs `libwayland-dev` and `libxkbcommon-dev`;
`make gui GUI_TAGS="gui x11"` builds an X11-only binary without them.

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

## Energy counters and watt-hour measurement

Added on `feature/energy-counters`.

**Daemon.** A new `power` collector (`internal/collectors/power`) reads
every RAPL zone the platform exposes and, where a battery fuel gauge
exists, whole-system power. It emits cumulative joules since daemon
start, labelled by `domain`. The NPU collector additionally emits its
own cumulative energy. All new metrics are labelled or live in the new
`power` module, so the existing headline metrics, `--text` goldens, the
web UI and the snapshot are unchanged; `--text` gains a `Power` section
on real hardware. The CPU and GPU collectors are untouched (they still
derive `power` from package and uncore as before).

Adaptivity is structural, not error handling: each source is optional
and absent sources produce no metrics. Observed so far:

| Device | RAPL domains | System source |
|---|---|---|
| NUC (Core Ultra 5 335) | package, core, uncore, dram, psys | psys |
| Seco F36 (same SoC, battery) | package, core, dram, uncore via PMT (no RAPL uncore, no psys) | battery, while discharging |

A single-tick energy jump implying more than 2 kW is dropped. This
covers counter resets across suspend and counters that wrap earlier than
`max_energy_range_uj` advertises (a known issue with `psys` on current
firmware), at the cost of one tick of data per event.

**Clients.** Session logic is client-side (`internal/energy`): take a
reading, take another, subtract. The daemon holds no session state, so
any number of viewers and scripts can measure independently, and an old
viewer against a new daemon (or the reverse) degrades to "no energy
counters" rather than failing. Three triggers share that logic: the TUI
stopwatch (`--tui --energy`, keys s/t/b/c), `--measure -- command` or
`--measure --for 5m`, and the GUI panel (`topswatch-gui --energy`). A
timed recording (the "Record 5 min" button, `t`, `--record` to change
the length) stops itself after that much sample time, for a demo that is
already running and cannot be wrapped; verified on the Dell with a 20 s
recording (stopped at 21.0 s, the next sample past the target).

**Breakdown.** RAPL domains nest (package contains core, uncore and the
NPU; DRAM is outside it), so the report shows components plus remainders
("SoC other", "Rest of system") and the rows sum to the totals. Where a
board has no `uncore` domain the remainder is labelled "SoC other+GPU".
On the NUC, package tracked core + uncore with a steady ~2 W remainder
from idle (2.4 W) to full CPU load (49 W), DRAM moved independently under
memory load, and psys stayed above package throughout, which supports
the nesting. That the NPU is inside the package figure is assumed from
it being on the same die; it has not been verified under an NPU load.

**Battery validity.** Battery current equals system draw only while the
battery is actually powering the device. The daemon accumulates battery
energy only in that state and also exports how many seconds it covered;
a client accepts the battery figure for a window only if the coverage
matches the elapsed time, so a run that was on external power for any
part of it reports no system total.

The gauge's "Discharging" status is not sufficient evidence of that
state. On the F36, with a full battery and power arriving through a path
the firmware does not report as the adapter, the gauge said
"Discharging" with the adapter flagged offline while supplying about
0.3 W against a SoC drawing 7-15 W, and its charge counter did not move.
The first build trusted the label and reported a 0.5 W "system total".
The daemon now compares battery power with SoC package power: a battery
reading below half the package power is shown immediately as "on
external power", and after three such samples in a row the battery stops
accumulating (the run length tolerates a gauge lagging a load step).
Clients independently reject a battery total smaller than 0.8x the SoC
total for the same window. Verified on the F36 in that state: the daemon
logs the condition, `--text`, `--measure`, the odometer script and
Prometheus all report no system total, and percentages are computed
against the SoC total.

Verified on the F36 on genuine battery power as well:

| Check | Result |
|---|---|
| Daemon battery power vs. gauge V x I, 5 samples | identical (20.2-20.4 W) |
| Coverage over 20 s windows, idle and under load | 20.0 s of 20.0 s |
| Idle | system 20.6 W, of which SoC 2.4 W and DRAM 0.15 W |
| 8-thread load | system 37.8 W, of which SoC 15.0 W |
| 31 s measured run | 321.6 mWh system, 147.7 mWh above idle |
| Cross-check against the gauge's own charge counter over baseline + run | 452 mWh by charge counter vs. about 450 mWh integrated (within the counter's 1 mAh step) |

On this handheld the SoC is a small part of the story: at idle 88% of
the draw is "Rest of system" (display, storage, radios, conversion), and
it rises by about 4.5 W under CPU load (fan and regulator losses), which
RAPL alone would never show. That run also illustrated why the baseline
must be taken like-for-like: a baseline taken straight after a load read
21.9 W instead of 20.2 W while the device was still warm.

**GUI energy panel.** Run on the F36's 1280x800 display. The panel sits
beside the dashboard on a wide window and beneath it on a tall one (an
adaptive split, since a stacked layout does not fit a landscape
handheld). Start/Stop, Idle baseline and Clear were exercised there:
baseline capture, a live table while measuring, and a frozen result on
stop, with the system total coming from the battery gauge. The buttons
carry their keys (s, b, c), the same as the TUI, and the README
screenshot was taken by driving those keys on the Dell XPS 14 on
battery.

## Dashboard changes after the first energy runs (2026-10-02)

Prompted by looking at the TUI and GUI on the F36, with both devices as
test beds. Each was checked against Intel's published telemetry
definitions (github.com/intel/Intel-PMT, `xml/PTL/0`) and against
measurements.

**GPU power was missing on the F36.** Its kernel registers no RAPL
`uncore` zone and no perf `energy-gpu` event on this boot, although the
MSR behind them counts normally (the NUC has all three). Intel's SoC
telemetry region (GUID 0x3086000, the one the NPU collector already
reads) carries `VCCGT_ENERGY`, documented as the same counter as the
uncore MSR, and measured identical to it under load (6.24 W). The GPU
collector and the power collector now fall back to that counter
(`internal/collectors/socpmt`, with offsets for MTL, LNL and PTL keyed by
GUID) when the RAPL zone is absent; RAPL is still preferred where it
exists. Result on the F36: GPU 0.58 W idle, 6.3 W under GPU load, and the
energy breakdown gains its GPU row there.

Why the zone goes missing, established across four Panther Lake
machines (NUC 335 and F36 on the same 6.18-intel build, a Dell XPS 14 on
6.18.23, an ASUS NUC 358H on 7.0.0): the kernel's `intel_rapl_msr`
driver checks each domain's counter once at module load and drops any
that reads zero, which the GPU's does while it is still power-gated
early in boot. It is a race between module load and GPU initialisation,
not a kernel-version feature: the F36 had the zone on one boot and not
the next, and on the Dell `modprobe -r intel_rapl_msr && modprobe
intel_rapl_msr` after the GPU had been used made the zone (and the perf
`energy-gpu` event) appear at once. Two of the four machines lacked it
at the time of testing.

**NPU temperature read the wrong sensor on Panther Lake.** The SoC
temperature word packs one sensor per byte; the code used bits [40:47]
for every generation, which is `VPU_TEMP` on Meteor Lake and Lunar Lake
but `Media_TEMP` on Panther Lake, where the VPU sensor moved to [32:39].
Fixed for PTL only. The corrected reading rises with NPU load (36 to
44 C) and the old one barely did.

**NPU DDR bandwidth** was validated under a synthetic NPU workload:
the daemon's 47 GB/s against 47.6 GB/s computed from the workload's own
inference rate and weight size. Intel types the PTL/LNL counter as
1024-byte units; the code divided by 1000 (right for MTL's `tbw_KB`), a
2.4% error now corrected per generation.

**NPU power is an estimate, and a low one.** `VPU_ENERGY` is documented
as "estimated by Pcode using utilization factor". On the F36 at 90%
NPU utilization it reports 0.38 W while package power rises by about
6 W with the cores idle; the format (U18.14) is the same one that makes
the package and core counters in that region match RAPL to three
decimals, so this is the firmware's estimate, not a conversion error.
Left as is and documented; the "SoC other" row of the energy breakdown
absorbs the difference.

**One temperature.** The CPU package sensor is the hottest reading in
every state measured on both devices (idle, CPU, GPU and NPU load; e.g.
97 C vs the NPU's 67 C under CPU load, 59 vs 42 under GPU load), and no
Xe part here exposes a GPU sensor. The TUI, GUI and snapshot now show a
single "SoC temp" on the CPU row and no temperature on the NPU and GPU
rows. The daemon still exports all of them; the web UI is unchanged.

**System memory.** The CPU collector now reports `memory_used`,
`memory_total` and `memory_used_percent` from `/proc/meminfo`. The
viewers show a memory card on the CPU row and a stacked bar under it:
GPU buffers (which on an integrated GPU live in system RAM and are
already inside "used"), the rest of used, and free.

The snapshot golden was regenerated for the new layout, as was the
`--text` golden for the memory lines.

**psys calibrated against a battery gauge** (Dell XPS 14, the one
machine with both): psys 6.42 W vs battery 6.26 W at idle (+2.5%), 55.8 W
vs 60.6 W under a 16-thread load (-8%). That is the error bar for the
system totals on the NUCs, which have no battery. The cable coming out
12 s into a 60 s measurement was observed to make the report drop the
gauge and fall back to psys, now with a note saying why.

## Daemon efficiency

A 40 s CPU profile of the idle daemon on the NUC (1s interval, ~295
processes; `--pprof 127.0.0.1:6060` is a new opt-in flag for this):

| Where | Share of daemon CPU |
|---|---|
| Per-process walk of `/proc` (GPU/NPU client discovery, top CPU processes) | 76% |
| CPU collector (per-core frequency reads etc.) | 7% |
| NPU, GPU, power collectors, history, everything else | under 5% combined |

The hardware counters themselves are cheap; the cost is walking every
process every second. Within that walk: listing each process's open
files and `readlink` on every one of them (~870 per tick), reading each
process's name, and reading each process's `stat`.

**Done, no behaviour change:** process names are resolved only for the
few processes that hold a GPU or NPU handle instead of all of them, and
`/proc` is listed once per sample instead of twice. Idle daemon CPU went
from 2.45% to about 2.1% of a core, and the process table matched the
unmodified daemon running on the same machine.

**Done, configurable: `collector.process_rescan` / `--process-rescan`.**
The walk of the whole process table (open-file scan for GPU/NPU handles,
and reading every process's CPU time for the top-CPU list) runs every
`process_rescan` instead of every sample. Between walks only the handles
already known are re-validated and re-read, one handle per DRM client
(duplicate handles onto the same client are dropped at scan time rather
than read and discarded every sample). Measured on the NUC, 60 s windows,
~290 processes, 1s interval:

| Configuration | Daemon CPU | vs. scan every sample |
|---|---|---|
| scan every sample (`0`, previous behaviour) | 2.17% | |
| open-file rescan only, 5s | 1.62% | -25% |
| open-file rescan only, 10s | 1.50% | -31% |
| rescan + top-CPU list on the same cadence, 5s | 0.85% | -61% |
| rescan + top-CPU list on the same cadence, 10s | 0.80% | -63% |

In every run the GPU client table was identical to the unmodified
daemon's, a client that exited vanished on the next sample (1 s), and a
newly started GPU client appeared within the rescan interval (between 1 s
and 11 s depending on where in the cycle it started).

The default is `5s`: it captures almost all of the saving (10s gains
another 0.05 points) at half the worst-case discovery delay. `0` restores
the old behaviour exactly. What changes for a user at 5s: a new GPU/NPU
process can take up to 5 s to show in the process table, and the top-CPU
list updates every 5 s (its percentages are then averages over 5 s).
Per-client GPU busy and memory, and every headline metric, are still
per-sample.

The web UI says so: each sample carries `processes.rescan_seconds`, and
the process panels show "updates every 5s, averaged" beside the top-CPU
title and "usage live · new processes appear within 5s" beside the GPU
one. With `process_rescan: 0` the field is absent and no note is shown.

**Options not taken:**

| Option | Estimated saving | Trade-off |
|---|---|---|
| Skip the process walk entirely when no viewer is attached and Prometheus has not scraped recently | most of what remains of it | first sample after attaching has no process deltas |
| Raise `collector.interval` | linear | coarser charts for everything |

At 5s the remaining cost is spread thinly: reading the known GPU
clients' `fdinfo` each sample, per-core CPU frequency reads, and the
periodic walk itself.

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
