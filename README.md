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
and Grafana monitoring stack.

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

## Output Modes

- **`--text`** — Collect metrics twice (1s apart for deltas), print to stdout, exit.
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
`--interval`).

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
docker run --privileged --pid=host \
  -v /sys:/sys:rw \
  -v /proc:/proc:ro \
  -p 9876:9876 topswatch
```

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
