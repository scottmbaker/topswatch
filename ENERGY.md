# Building an energy odometer on TopsWatch

This guide is for anyone, human or coding agent, who wants to answer:

> I ran a demo. How much energy did the demo itself use, versus what the
> platform would have used anyway at its steady state?

Think of a car's trip meter. The daemon is the odometer: it counts energy
continuously from the moment it starts and never resets. A trip is two
readings and a subtraction. Nothing has to be "armed" in advance and any
number of trips can overlap.

If you just want the answer, three ready-made front ends exist (see
[README](README.md#measuring-energy-watt-hours)):

| You want to | Use |
|---|---|
| wrap one command | `topswatch --measure --baseline 30s -- ./demo.sh` |
| start and stop by hand while watching | `topswatch --tui --energy` (`b` baseline, `s` start/stop) |
| start and stop from separate scripts or agents | [`examples/energy_odometer.py`](examples/energy_odometer.py) |

The rest of this document explains what they do, so you can build your
own or check their numbers.

## The recipe

```bash
# 0. A daemon must be running as root (sudo, or the Docker/Helm deployment).
export TOPSWATCH_URL=http://localhost:9876

# 1. With the device idle and in the state the demo will run in
#    (same display brightness, same peripherals, same network), measure
#    steady state. 30 s is a reasonable minimum.
examples/energy_odometer.py baseline --seconds 30

# 2. Zero the trip meter and run the demo.
examples/energy_odometer.py start
./run-demo.sh

# 3. Read the result.
examples/energy_odometer.py stop
```

Output from a real run (Core Ultra 5 335, 20 s of 4-thread load after a
20 s baseline):

```
Trip: 21.0s
                        total    avg W  steady state  demo itself
  CPU cores         190.0 mWh    32.58       0.6 mWh    189.5 mWh
  GPU                 3.4 mWh     0.59       3.3 mWh      0.1 mWh
  NPU                 0.0 mWh     0.00       0.0 mWh      0.0 mWh
  SoC other          12.1 mWh     2.07      10.8 mWh      1.3 mWh
SoC total           205.6 mWh    35.24      14.7 mWh    190.8 mWh
  DRAM                2.9 mWh     0.49       2.8 mWh      0.0 mWh
  Rest of system     69.0 mWh    11.84      27.6 mWh     41.4 mWh
System total        277.5 mWh    47.56      45.1 mWh    232.3 mWh

System total: the demo itself used 232.3 mWh on top of 45.1 mWh the platform
would have used anyway (7.74 W steady state), 277.5 mWh in all.
```

`read` reports without stopping, `--json` gives the same data for
machines, and `reset` forgets the trip and the baseline.

That device has no battery, so its system total is the platform's RAPL
`psys` estimate. On a battery-powered handheld (Seco F36, same SoC) the
system total is measured by the fuel gauge and includes the display,
which changes the picture: the same kind of run there used 200.7 mWh in
all, of which the SoC was 80.5 mWh and "Rest of system" 118.5 mWh, with
a steady state of about 21 W that is almost entirely not the SoC.

## What the daemon gives you

`GET /api/metrics/latest` returns the most recent sample. Energy counters
are metrics named `energy`, unit `J`, with a `domain` label. They are
**joules accumulated since the daemon started**:

```json
{
  "timestamp": "2026-10-02T21:13:10.870615039Z",
  "metrics": {
    "power": [
      {"name": "energy", "value": 800.46,  "unit": "J", "labels": {"domain": "package"}},
      {"name": "energy", "value": 688.15,  "unit": "J", "labels": {"domain": "core"}},
      {"name": "energy", "value": 25.49,   "unit": "J", "labels": {"domain": "uncore"}},
      {"name": "energy", "value": 21.49,   "unit": "J", "labels": {"domain": "dram"}},
      {"name": "energy", "value": 1181.71, "unit": "J", "labels": {"domain": "psys"}}
    ],
    "npu": [
      {"name": "energy", "value": 0, "unit": "J", "labels": {"domain": "npu"}}
    ]
  }
}
```

| `domain` | Meaning | Where it comes from |
|---|---|---|
| `package` | the whole SoC | RAPL |
| `core` | CPU cores | RAPL |
| `uncore` | integrated GPU | RAPL, or the GT energy counter in Intel PMT where the kernel has no uncore zone |
| `npu` | NPU | PMT energy register (in the `npu` module) |
| `dram` | memory | RAPL |
| `psys` | whole platform, where the board reports it | RAPL |
| `battery` | whole device, measured by the fuel gauge | `power_supply`, **only accumulates while discharging** |

A battery device also emits `measured_seconds{domain="battery"}` (how
long the battery source has actually been measuring) and the plain
metrics `battery_discharging` (1 when the battery is powering the
device, 0 otherwise), `battery_capacity` (%) and `battery_voltage`.

Which domains exist depends on the hardware. Scan every module's metric
list for `name == "energy"` rather than hard-coding a set, and treat a
missing domain as "not available", never as zero.

The same counters stream over `GET /api/metrics/stream` (Server-Sent
Events, one sample per daemon interval) and are exported to Prometheus
as `topswatch_power_energy_joules_total{domain=...}` and
`topswatch_npu_energy_joules_total`.

## The arithmetic

**A trip** is `end - start` per domain. Divide joules by 3600 for
watt-hours, by elapsed seconds for average watts. Take elapsed time from
the two samples' `timestamp` fields, not from your own clock, so it
matches the counters exactly.

**Nesting.** RAPL domains overlap: `package` already contains `core`,
`uncore` and the NPU, and `psys`/`battery` contain everything. Adding
them up double counts. Report components and remainders instead:

```
CPU cores      = core
GPU            = uncore
NPU            = npu
SoC other      = package - core - uncore - npu
SoC total      = package
DRAM           = dram                     (outside the package)
Rest of system = system - package - dram  (display, storage, radios, losses)
System total   = battery, else psys, else not available
```

If `uncore` is missing, the GPU's energy is inside "SoC other"; say so.
The NPU figure is Intel firmware's own estimate and reads well below
the NPU's real draw on current Panther Lake firmware; the shortfall also
lands in "SoC other".
Display power cannot be separated; on a battery device it is part of
"Rest of system".

**Steady state versus the demo.** Measure a baseline trip while idle and
keep its average watts per row. For a later trip of `T` seconds:

```
steady state = idle_watts * T      what the platform would have used anyway
demo itself  = total - steady state
```

That subtraction is the whole trick, and its quality is the baseline's
quality. The daemon's own cost is in both and cancels.

## Rules that keep the number honest

1. **Choose the system source deliberately.** Prefer `battery` when it
   is valid, then `psys`, otherwise report the SoC total and say that no
   whole-device figure exists.
2. **Battery is only valid while it is powering the device.** On
   external power the gauge shows charging current, or nothing, not
   system draw. Check that `measured_seconds` advanced by the same amount
   as the elapsed time over your window (allow about half a second plus
   1%). If it did not, the device was externally powered for part of the
   trip: drop the system total for that trip. Do the baseline and the
   trip in the same power state.
3. **Do not trust the gauge's "Discharging" label on its own.** A full
   battery on a device fed through a port the firmware does not report
   as the adapter can say "Discharging" while supplying a fraction of a
   watt. The daemon checks for this (battery power far below SoC power
   means external power, and `battery_discharging` then reads 0), and a
   client should too: a battery total smaller than the SoC total for the
   same window cannot be the whole device. Reject it.
4. **A negative delta means the daemon restarted.** Counters restart from
   zero with it. Discard the trip for that domain; do not clamp to zero.
5. **`psys` must not be smaller than `package`.** If it is, the board
   does not report platform power usefully; fall back to the SoC total.
6. **Readings align to daemon samples.** `latest` can be up to one
   interval old, so a trip is accurate to about one interval at each end
   (1 s by default). To bracket tightly, read from the stream and take
   the first sample that arrives after your start and stop events, which
   is what `--measure` does.
7. **Short trips are noisy.** Under about 30 seconds the battery gauge's
   update rate and the sample alignment are a visible fraction of the
   result. Prefer longer runs, or repeat and average.
8. **Baseline like-for-like.** Idle should mean "the demo's environment
   with the demo not running": screen on at the same brightness, same
   containers and services up, thermally settled. A baseline taken
   straight after a heavy run reads high while fans and the SoC cool.
9. **These are estimates with known scope.** RAPL is the processor's
   model of SoC energy. `psys` excludes power-supply losses: measured
   side by side on a laptop with both sources, it read 2.5% high at idle
   and 8% low under a 16-thread load, so treat it as good to about 10%.
   Only the battery figure is a measurement of the whole device. Quote
   which one you used.

## Doing it without the example script

Two `curl` calls and `jq` are enough for a single domain:

```bash
read_j() { curl -s "$TOPSWATCH_URL/api/metrics/latest" |
  jq '[.metrics[][] | select(.name=="energy" and .labels.domain=="package")][0].value'; }

a=$(read_j); ./run-demo.sh; b=$(read_j)
echo "SoC: $(echo "($b - $a) / 3600" | bc -l) Wh"
```

In Prometheus/Grafana, for a time range instead of a start/stop pair:

```promql
# watt-hours used by the SoC over the dashboard's time range
increase(topswatch_power_energy_joules_total{domain="package"}[$__range]) / 3600

# average watts
rate(topswatch_power_energy_joules_total{domain="package"}[5m])
```

For a remote device, point `TOPSWATCH_URL` (or `--connect` for the
built-in tools) at it; the readings are taken on the device and nothing
about the arithmetic changes.

## Reference implementations

- [`examples/energy_odometer.py`](examples/energy_odometer.py): about
  200 lines of standard-library Python, the recipe above.
- [`internal/energy`](internal/energy): the Go version used by the TUI,
  the GUI and `--measure`, with tests for each rule in this document.
- [`internal/collectors/power`](internal/collectors/power): how the
  counters are produced, including the battery discharge accounting.
