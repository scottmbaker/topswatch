# TopsWatch Grafana Dashboard

Sample Grafana dashboard for visualizing TopsWatch Prometheus metrics.

## Import

1. In Grafana, go to **Dashboards > Import**
2. Upload `topswatch-dashboard.json` or paste its contents
3. Select your Prometheus datasource when prompted

## Prometheus scrape config

Add to your `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: topswatch
    scrape_interval: 5s
    static_configs:
      - targets: ['<topswatch-host>:8080']
```

The default TopsWatch metrics endpoint is `http://<host>:8080/metrics`.

## Panels

The dashboard is organized into four rows:

- **CPU** — utilization gauge, temperature, power, frequency, cores used, time series
- **GPU** — utilization gauge, temperature (if available), power (RAPL uncore), per-engine utilization, frequency bands
- **NPU** — utilization gauge, temperature, power, frequency, DDR bandwidth, memory usage, tile config
- **Device Info** — tables showing CPU/GPU/NPU identification labels

Auto-refreshes every 5 seconds with a 15-minute default window.
