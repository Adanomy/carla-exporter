# Grafana

Import dashboard ID **25974** or [carla-exporter.json](carla-exporter.json) through **Dashboards → New → Import** and choose any folder.
Select a **Datasource**, **Job**, and **Instance**. Jobs are discovered from `carla_exporter_build_info`; no scrape job name or datasource UID is prescribed. Vehicle role and actor filters affect vehicle panels only.

[Full dashboard preview](../docs/images/dashboard-full.png).

Set the Prometheus datasource's scrape interval to match the actual Prometheus job. Query resolution and rate windows follow Grafana's datasource settings. The dashboard refreshes every 30 seconds by default.

## Coordinates

The XY panel displays sampled CARLA world positions with **+X to the right and +Y upward**, using the original coordinates without sign changes. The coordinate table preserves the original X/Y/Z values in meters and includes a Z-history sparkline for the selected period; Z points upward in CARLA's left-handed world frame.

A map viewer can rotate or reflect its screen coordinates. Matching its image requires that viewer's world-to-image transform; a universal north-up or image-aligned orientation cannot be inferred from vehicle coordinates. XY axes autoscale independently, so pixel distances do not represent a fixed map scale.

Points follow the scrape interval and query resolution rather than every simulation frame. Vehicles and episodes remain separate; missing samples are not filled with zero.

See [CARLA coordinate conventions](https://carla.readthedocs.io/en/latest/coordinates/).

## Compatibility

Validated with Grafana **12.4.1** and Prometheus. Uses built-in panels only. Process and Go runtime metrics vary by operating system and Go version; unavailable metrics remain **No data**.
