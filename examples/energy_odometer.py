#!/usr/bin/env python3
"""Energy odometer for TopsWatch: a trip meter for a demo, test or benchmark.

It answers: how much energy did the demo itself use, versus what the
platform would have used anyway sitting at its steady state?

    energy_odometer.py baseline --seconds 30   # 1. measure steady state (device idle)
    energy_odometer.py start                   # 2. zero the trip meter
    ...run the demo...
    energy_odometer.py read                    #    (optional) peek while it runs
    energy_odometer.py stop                    # 3. report

State lives in a small JSON file, so start and stop can be separate
commands, separate scripts, or separate agents. Standard library only.
See ENERGY.md for the reasoning behind each step.
"""
import argparse
import json
import os
import sys
import time
import urllib.request
from datetime import datetime, timezone

DEFAULT_URL = os.environ.get("TOPSWATCH_URL", "http://localhost:9876")
DEFAULT_STATE = os.path.expanduser("~/.cache/topswatch-odometer.json")

# (key, label, is_total). Rows sum to the totals; see ENERGY.md "Nesting".
ROWS = [
    ("cpu", "CPU cores", False),
    ("gpu", "GPU", False),
    ("npu", "NPU", False),
    ("soc_other", "SoC other", False),
    ("soc", "SoC total", True),
    ("dram", "DRAM", False),
    ("rest", "Rest of system", False),
    ("system", "System total", True),
]


def parse_ts(ts):
    """Daemon timestamps are RFC 3339 with nanoseconds; keep microseconds."""
    ts = ts.replace("Z", "+00:00")
    if "." in ts:
        head, rest = ts.split(".", 1)
        n = 0
        while n < len(rest) and rest[n].isdigit():
            n += 1
        ts = f"{head}.{rest[:n][:6].ljust(6, '0')}{rest[n:]}"
    return datetime.fromisoformat(ts).astimezone(timezone.utc).timestamp()


def read(url):
    """One reading: cumulative joules per domain at the daemon's latest sample."""
    with urllib.request.urlopen(url.rstrip("/") + "/api/metrics/latest", timeout=5) as r:
        sample = json.load(r)
    joules, battery_seconds = {}, None
    for metrics in sample.get("metrics", {}).values():
        for m in metrics:
            domain = (m.get("labels") or {}).get("domain")
            if not domain:
                continue
            if m["name"] == "energy" and m.get("unit") == "J":
                joules[domain] = m["value"]
            elif m["name"] == "measured_seconds" and domain == "battery":
                battery_seconds = m["value"]
    if not joules:
        sys.exit(f"{url}: daemon reports no energy counters (needs a current topswatch "
                 "daemon running as root with collectors.power enabled)")
    return {"time": parse_ts(sample["timestamp"]), "joules": joules, "battery_seconds": battery_seconds}


def delta(a, b, domain):
    """Energy used in a domain between two readings, or None if unusable."""
    if domain not in a["joules"] or domain not in b["joules"]:
        return None
    d = b["joules"][domain] - a["joules"][domain]
    return d if d >= 0 else None  # negative: the daemon restarted in between


def breakdown(a, b):
    """Joules per display row between two readings, plus where 'system' came from."""
    seconds = b["time"] - a["time"]
    core, gpu, npu = delta(a, b, "core"), delta(a, b, "uncore"), delta(a, b, "npu")
    pkg, dram = delta(a, b, "package"), delta(a, b, "dram")

    system, source, why = None, "", ""
    if a.get("battery_seconds") is not None and b.get("battery_seconds") is not None:
        covered = b["battery_seconds"] - a["battery_seconds"]
        bat = delta(a, b, "battery")
        full = bat is not None and seconds > 0 and covered >= seconds - (0.5 + 0.01 * seconds)
        if full and pkg is not None and bat < 0.8 * pkg:
            # The whole device cannot use less than its SoC: the gauge says
            # "discharging" but the battery is not what is powering it.
            why = "battery reports less energy than the SoC alone; device is on external power"
        elif full:
            system, source = bat, "battery"
        else:
            why = "on external power for part of the window"
    if system is None:
        psys = delta(a, b, "psys")
        if psys is not None and (pkg is None or psys >= pkg):
            system, source, why = psys, "psys", ""
        elif not why:
            why = "no battery gauge and no usable psys on this device"

    rows = {"cpu": core, "gpu": gpu, "npu": npu, "soc": pkg, "dram": dram, "system": system}
    rows["soc_other"] = None if pkg is None or core is None else max(pkg - core - (gpu or 0) - (npu or 0), 0)
    rows["rest"] = None if system is None or pkg is None else max(system - pkg - (dram or 0), 0)
    return {"seconds": seconds, "joules": rows, "system_source": source, "system_note": why,
            "gpu_in_other": gpu is None and rows["soc_other"] is not None}


def load(path):
    try:
        with open(path) as f:
            return json.load(f)
    except FileNotFoundError:
        return {}


def save(path, state):
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    with open(path, "w") as f:
        json.dump(state, f)


def wh(j):
    return f"{j / 3600:9.3f} Wh" if abs(j) >= 3600 else f"{j / 3.6:8.1f} mWh"


def report(state, end, as_json):
    trip = breakdown(state["start"], end)
    idle = state.get("baseline")  # {"watts": {row: W}, "seconds": N} or None
    secs = trip["seconds"]
    out = {"seconds": secs, "system_source": trip["system_source"], "system_note": trip["system_note"],
           "baseline_seconds": idle["seconds"] if idle else 0, "rows": []}
    for key, label, total in ROWS:
        j = trip["joules"][key]
        if key == "soc_other" and trip["gpu_in_other"]:
            label = "SoC other+GPU"
        row = {"key": key, "label": label, "total": total, "available": j is not None}
        if j is not None:
            row.update(joules=j, avg_watts=j / secs if secs > 0 else 0)
            w = (idle or {}).get("watts", {}).get(key)
            if w is not None:
                row.update(idle_watts=w, steady_joules=w * secs, demo_joules=j - w * secs)
        out["rows"].append(row)

    if as_json:
        print(json.dumps(out, indent=2))
        return

    print(f"Trip: {secs:.1f}s")
    head = f"{'':16s} {'total':>12s} {'avg W':>8s}"
    if idle:
        head += f" {'steady state':>13s} {'demo itself':>12s}"
    print(head)
    for row in out["rows"]:
        name = row["label"] if row["total"] else "  " + row["label"]
        if not row["available"]:
            print(f"{name:16s} {'n/a':>12s}")
            continue
        line = f"{name:16s} {wh(row['joules']):>12s} {row['avg_watts']:8.2f}"
        if "demo_joules" in row:
            line += f" {wh(row['steady_joules']):>13s} {wh(row['demo_joules']):>12s}"
        print(line)

    top = next((r for r in reversed(out["rows"]) if r["total"] and r["available"]), None)
    print()
    if top and "demo_joules" in top:
        print(f"{top['label']}: the demo itself used {wh(top['demo_joules']).strip()} on top of "
              f"{wh(top['steady_joules']).strip()} the platform would have used anyway "
              f"({top['idle_watts']:.2f} W steady state), {wh(top['joules']).strip()} in all.")
    elif top:
        print(f"{top['label']}: {wh(top['joules']).strip()} in all. Run 'baseline' first to split "
              "out what the platform uses at steady state.")
    if trip["system_source"] == "battery":
        print("System total is measured by the battery gauge (whole device, display included).")
    elif trip["system_source"] == "psys":
        print("System total is RAPL psys (platform estimate; excludes power-supply losses).")
    else:
        print(f"No system total: {trip['system_note']}. SoC total is the widest figure available.")


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--url", default=DEFAULT_URL, help="daemon URL (default %(default)s, or $TOPSWATCH_URL)")
    ap.add_argument("--state", default=DEFAULT_STATE, help="state file (default %(default)s)")
    sub = ap.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("baseline", help="measure steady-state power; keep the device idle meanwhile")
    b.add_argument("--seconds", type=float, default=30)
    sub.add_parser("start", help="zero the trip meter")
    for name, text in (("read", "report without stopping"), ("stop", "report and stop")):
        p = sub.add_parser(name, help=text)
        p.add_argument("--json", action="store_true")
    sub.add_parser("reset", help="forget the trip and the baseline")
    args = ap.parse_args()

    state = load(args.state)
    if args.cmd == "reset":
        save(args.state, {})
        print("odometer reset")
    elif args.cmd == "baseline":
        a = read(args.url)
        print(f"measuring steady state for {args.seconds:.0f}s; keep the device idle...", file=sys.stderr)
        time.sleep(args.seconds)
        bd = breakdown(a, read(args.url))
        if bd["seconds"] <= 0:
            sys.exit("no new daemon sample arrived during the baseline; is the daemon collecting?")
        watts = {k: j / bd["seconds"] for k, j in bd["joules"].items() if j is not None}
        state["baseline"] = {"watts": watts, "seconds": bd["seconds"]}
        save(args.state, state)
        widest = "system" if "system" in watts else "soc"
        print(f"steady state: {watts[widest]:.2f} W ({widest} total) over {bd['seconds']:.0f}s")
    elif args.cmd == "start":
        state["start"] = read(args.url)
        save(args.state, state)
        print("trip started")
    else:  # read / stop
        if "start" not in state:
            sys.exit("no trip in progress; run 'start' first")
        report(state, read(args.url), args.json)
        if args.cmd == "stop":
            del state["start"]
            save(args.state, state)


if __name__ == "__main__":
    main()
