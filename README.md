# TopsWatch

Hardware telemetry monitor for Edge AI devices. Specifically designed to
work with the Intel Core Ultra Series 3 Panther Lake CPUs, including
retrieving metrics from the NPU and GPU. May also work with earlier
hardware generations such as Meteor Lake, Lunar Lake, and Arrow Lake, but
these have not been tested. Linux only. Single Go binary,
no external runtime dependencies. Reads CPU, GPU, and NPU metrics directly
from sysfs, hwmon, Intel PMT, RAPL, and `perf_event_open`.

The monitor exports a web UI that may be consumed directly, and it also
exports a Prometheus endpoint that may be used with the typical Prometheus
and Grafana monitoring stack. For lighter-weight viewing there is a
terminal dashboard (`--tui`, works over SSH) and a small desktop viewer
(`topswatch-gui`); both attach to a running daemon, locally or remotely.
See [Viewers](#viewers).

![topswatch-gui showing the CPU, NPU and GPU dashboard on a Panther Lake device](docs/images/topswatch-gui.png)

*The desktop viewer on a Core Ultra 5 335. The web UI shows the same
cards and charts plus per-core, per-engine and process detail; the
terminal viewer shows them in text (see [Viewers](#viewers)).*

Despite the name, TopsWatch doesn't actually monitor TOPS. It monitors
the behavior of hardware. AI thought TopsWatch would be a catchy name,
and I agreed!

Disclaimer: This application is intended for experimental and educational
use only. No warranty expressed or implied. This application is not
represented as a benchmark and is not intended to make any performance
claim.

## Metrics

### CPU

| Metric | Unit | Source |
|--------|------|--------|
| Utilization | % | `/proc/stat` (delta) |
| Cores Used | cores | `/proc/stat` (Irix-style) |
| Frequency | MHz | `cpufreq/scaling_cur_freq` (mean) |
| Power | W | RAPL `package` energy_uj (delta) |
| Temperature | °C | hwmon `coretemp`/`k10temp` |
| Per-core utilization | % | `/proc/stat` per-cpu lines |
| Per-core frequency | MHz | per-cpu `scaling_cur_freq` |

Per-core metrics carry `core` and `core_type` (`performance`/`efficient`/`low_power`) labels.

### GPU

| Metric | Unit | Source |
|--------|------|--------|
| Utilization | % | `perf_event_open` engine-active/total ticks (xe) or busy-ns (i915) |
| Per-engine busy | % | Same, per engine label |
| Frequency (actual/requested/min/max/rp0/rpe/rpn) | MHz | Xe sysfs `tile*/gt*/freq0/` or i915 `gt_*_freq_mhz` |
| Temperature | °C | hwmon `temp*_input` (when xe_hwmon present) |
| Power | W | RAPL `uncore` energy_uj (delta) |

Supports both the Xe driver (Panther Lake, Lunar Lake) and i915 driver
(older platforms) automatically.

### NPU

| Metric | Unit | Source |
|--------|------|--------|
| Utilization | % | sysfs `npu_busy_time_us` (delta) |
| Frequency | MHz | PMT `VPU_WORKPOINT` register |
| Power | W | PMT `VPU_ENERGY` register (delta, U18.14 fixed-point) |
| Temperature | °C | PMT `SOC_TEMPERATURES` register |
| DDR Bandwidth | MB/s | PMT `VPU_MEMORY_BW` register (delta, bw_KB) |
| Tile Config | count | PMT `VPU_WORKPOINT` register |
| Memory Used | bytes | sysfs `npu_memory_utilization` (PTL+) |

See [METRICS.md](METRICS.md) for full details on sources, computation,
and Prometheus metric names.

## Usage

```bash
# Build
make build

# One-shot text output
./topswatch --text

# Start web server + Prometheus (default)
./topswatch

# Custom config
./topswatch --config /path/to/topswatch.yaml

# Override port
./topswatch --port 8080
```


## Viewers

TopsWatch has four ways to look at the data. The daemon collects once;
every viewer attaches to it.

| Viewer | Where it runs | Needs | Best for |
|---|---|---|---|
| Web UI (`/`) | any browser | nothing extra | full dashboard: per-core, per-engine, processes, warnings |
| Terminal `--tui` | any terminal, incl. SSH | the `topswatch` binary | remote shells, headless devices, low overhead |
| Desktop `topswatch-gui` | Ubuntu Desktop (X11/Wayland) | separately built binary | a small always-on window on the device itself |
| `--text` | any terminal | the `topswatch` binary | one-shot readout, scripts |

The TUI and GUI are **clients**: they read the daemon's HTTP API and never
touch hardware, so they need no root, no config file, and no special
permissions. They default to `localhost:9876` and take `--connect` for
another host.

### Viewer overhead

Measured on a Core Ultra 5 335 (Panther Lake) running Ubuntu 24.04 with
the daemon at its default 1s interval. Numbers are the **extra** CPU each
viewer adds to the machine, as a percentage of one core, including what
it costs the daemon and the display stack. The daemon alone uses about
2.8%.

| Viewer | Added CPU | Notes |
|---|---|---|
| Terminal `--tui` over SSH | ~2% | 1.3% streaming, 0.9% with `--refresh 5s` |
| Terminal `--tui` in a desktop terminal window | ~2.5% | terminal emulator and compositor add under 1% |
| Desktop `topswatch-gui` | ~4.5% at 1s, ~2.5% at 5s | the difference is the daemon rendering a JPEG per fetch |
| Web UI in Firefox | ~40% | browser 18%, Xorg 14%, compositor 5% |

Full tables, method and caveats are in [VIEWERS.md](VIEWERS.md).

### Terminal viewer (`--tui`)

![topswatch --tui in a 120x40 terminal](docs/images/topswatch-tui.png)

Built into the daemon binary. Start a daemon first (as root, since RAPL,
PMT and perf need it), then attach from any terminal:

```bash
sudo ./topswatch                 # daemon, in one shell or as a service
./topswatch --tui                # viewer, in another shell or over SSH
```

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `--connect ADDR` | `localhost:9876` | daemon to attach to: `host`, `host:port`, `[v6addr]:port`, or `http://…` |
| `--refresh N` | `0` (stream) | `0` follows the daemon's SSE stream and redraws once per sample; `N` (e.g. `5s`) polls instead, for a slower, cheaper display |

Keys: `q` quit · `r` cycle history range (5min / 1h / 24h) · `p` pause.

The layout adapts to the terminal: 40 rows or more shows cards, sparklines
and a braille chart per module; below about 20 rows the charts are
dropped and only the cards remain. Colours need a 256-colour terminal
(`TERM=xterm-256color` is typical).

Examples:

```bash
# Over SSH, on the device
ssh user@device ./topswatch --tui

# From your laptop, against a remote daemon (the binary runs anywhere Go does)
./topswatch --tui --connect device.local

# IPv6 literal and a slower poll
./topswatch --tui --connect '[fe80::1%eth0]:9876' --refresh 5s
```

If the daemon is not reachable the TUI shows the error in its footer and
keeps retrying; it fills in as soon as the daemon answers.

### Desktop viewer (`topswatch-gui`)

A small window that shows the daemon's own rendered dashboard
(`/snapshot.jpg`) and refreshes it on a timer. It has no rendering logic
of its own, so it always matches the web snapshot. It is a separate
binary built with a build tag, because the Fyne toolkit needs cgo and
OpenGL/X11 headers; the daemon binary stays static and dependency-free.

```bash
# Build once, on a machine with a desktop toolchain
sudo apt install gcc libgl1-mesa-dev xorg-dev
make gui                                  # produces ./topswatch-gui

# Run
./topswatch-gui                           # local daemon, 1s refresh
./topswatch-gui --connect device.local --refresh 2s
```

Flags are `--connect` (as above) and `--refresh` (default `1s`). `q` or
`Escape` closes the window. The status bar shows the daemon address, the
time of the last update, and any connection error.

### Using the viewers with the Docker deployment

The container image contains only the `topswatch` binary (no shell, no
GUI). There are two ways to use the TUI with it.

**Run the TUI inside the running container.** Give the container a name
when you start it, then `exec` the same binary in viewer mode. It attaches
to the daemon over the container's own loopback:

```bash
docker run -d --name topswatch --privileged --pid=host \
  -v /sys:/sys:rw -v /proc:/proc:ro -p 9876:9876 topswatch

docker exec -it -e TERM=xterm-256color topswatch /topswatch --tui
```

`-it` gives the TUI a terminal and `-e TERM` gives it colours; `-e
COLUMNS`/`-e LINES` are not needed, the size is taken from your terminal.

**Run a viewer on the host (or anywhere) against the published port.**
Build or download the `topswatch` binary on the host and point it at the
port you published with `-p`:

```bash
./topswatch --tui --connect localhost:9876
./topswatch-gui --connect localhost:9876
```

This is the only way to use the desktop viewer with Docker, since the
image cannot open a window. With `--net-host` (as in the `ctr` example
below) the daemon is on the host's `9876` directly and the same commands
apply.

### Using the viewers with the Helm chart

The DaemonSet exposes the daemon on every node at the NodePort (default
`30987`), so from any machine that can reach a node:

```bash
./topswatch --tui --connect node-1.example:30987
./topswatch-gui --connect node-1.example:30987
```

Or forward a pod's port and use a viewer on your own machine:

```bash
kubectl port-forward ds/topswatch 9876:9876
./topswatch --tui            # or ./topswatch-gui
```

You can also `kubectl exec -it <pod> -- /topswatch --tui` to run the TUI
inside a pod. `kubectl exec` does not pass your `TERM` through and the
distroless image has no way to set it, so expect a monochrome display
that way; `port-forward` gives the full-colour one.

## Output Modes

- **`--text`** — Collect metrics twice (1s apart for deltas), print to stdout, exit.
- **`--tui`** — Terminal dashboard attached to a running daemon
  (`--connect`, `--refresh`); collects nothing itself. See [Viewers](#viewers).
- **Default** (no flags) — Start HTTP server with web dashboard, JSON API,
  SSE stream, and Prometheus endpoint.

## Endpoints

| Path | Description |
|------|-------------|
| `/` | Web dashboard with real-time charts |
| `/api/metrics/latest` | Latest sample as JSON |
| `/api/metrics/history?range=5min\|1h\|24h` | Downsampled history as JSON |
| `/api/metrics/stream` | Server-Sent Events stream |
| `/api/metrics/ranges` | Available history tier names |
| `/api/devices` | Device info as JSON |
| `/metrics` | Prometheus exposition format |
| `/snapshot.jpg` | JPEG snapshot of current state |

## Configuration

```yaml
server:
  address: "0.0.0.0"
  port: 9876

collector:
  interval: 1s
  history: 300

collectors:
  cpu:
    enabled: true
  gpu:
    enabled: true
  npu:
    enabled: true
```

All config values can be overridden via CLI flags (`--address`, `--port`,
`--interval`). Viewer modes (`--tui`, `topswatch-gui`) do not read the
config file; they take `--connect` and `--refresh` only.

## Supported Platforms

### NPU

| Generation | PCI ID | PMT | sysfs |
|-----------|--------|-----|-------|
| Meteor Lake | `0x7d1d` | Yes | Yes |
| Arrow Lake | `0xad1d` | Yes | Yes |
| Lunar Lake | `0x643e` | Yes | Yes |
| Panther Lake | `0xb03e` | Yes | Yes |

### GPU

Any Intel GPU exposed via `/sys/class/drm/` with vendor ID `0x8086`.
Frequency metrics require the Xe or i915 kernel driver. Temperature
requires xe_hwmon (not yet available on PTL integrated GPUs). Power
uses RAPL uncore as a workaround.

## Docker

Build and run with Docker:

```bash
make docker
docker run --name topswatch --privileged --pid=host \
  -v /sys:/sys:rw \
  -v /proc:/proc:ro \
  -p 9876:9876 topswatch
```

To view from a terminal while it runs, see
[Using the viewers with the Docker deployment](#using-the-viewers-with-the-docker-deployment).

The container needs host access to:

- `/sys` (read-write) — sysfs, hwmon, PMT, RAPL, DRM, cpufreq. PMT telem
  files require write access to read.
- `/proc` (read-only) — CPU utilization, process attribution, memory info
- `--pid=host` — see host processes for GPU/NPU/CPU process attribution
- `--privileged` — `perf_event_open` (GPU utilization) and debugfs (NPU firmware version)

### Deploying to a remote node

To run the container on a machine without a registry (e.g. k3s with
containerd):

```bash
# On the build machine
make docker
docker save topswatch:latest | gzip > topswatch-image.tar.gz
scp topswatch-image.tar.gz node:~/

# On the target node (containerd / k3s)
sudo k3s ctr images import ~/topswatch-image.tar.gz
sudo k3s ctr run --privileged \
  --mount type=bind,src=/sys,dst=/sys,options=rbind:rw \
  --mount type=bind,src=/proc,dst=/proc,options=rbind:ro \
  --net-host \
  docker.io/library/topswatch:latest topswatch
```

## Helm Chart

A Helm chart is provided in [`chart/`](chart/). It deploys TopsWatch as a
DaemonSet with `hostPID` and privileged access.

```bash
helm install topswatch ./chart
```

The web UI and Prometheus endpoint are exposed via NodePort (default 30987).
See [`chart/README.md`](chart/README.md) for the full values reference.
