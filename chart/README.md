# TopsWatch Helm Chart

Deploys TopsWatch as a DaemonSet with host-level access for hardware telemetry.

## Prerequisites

The TopsWatch image must be available to the cluster. For k3s with locally-built
images:

```bash
# On the build machine
cd topswatch
make docker
docker save topswatch:latest | gzip > topswatch-image.tar.gz
scp topswatch-image.tar.gz k3s-node:~/

# On the k3s node
sudo k3s ctr images import ~/topswatch-image.tar.gz
```

## Install

```bash
helm install topswatch ./chart
```

The web UI and Prometheus endpoint will be available on every node at port 30987
(configurable via `service.nodePort`).

## Uninstall

```bash
helm uninstall topswatch
```

## Host access

The DaemonSet runs with:

- **`hostPID: true`** — container sees host processes via `/proc`, needed for
  GPU/NPU/CPU process attribution
- **`privileged: true`** — needed for `perf_event_open` (GPU utilization),
  PMT telem reads, and debugfs (NPU firmware version)
- **`/sys` mounted read-write** — PMT telem files require write access to read

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `topswatch` | Container image repository |
| `image.tag` | `latest` | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `service.type` | `NodePort` | Service type |
| `service.port` | `9876` | Service port |
| `service.nodePort` | `30987` | NodePort (set to `null` for auto-assign) |
| `config.server.address` | `0.0.0.0` | Bind address |
| `config.server.port` | `9876` | Listening port |
| `config.collector.interval` | `1s` | Poll interval |
| `config.collector.history` | `300` | Ring buffer size |
| `config.collectors.cpu.enabled` | `true` | Enable CPU module |
| `config.collectors.gpu.enabled` | `true` | Enable GPU module |
| `config.collectors.npu.enabled` | `true` | Enable NPU module |
| `resources` | `{}` | Resource limits/requests |
| `nodeSelector` | `{}` | Node selector |
| `tolerations` | `[]` | Tolerations |
