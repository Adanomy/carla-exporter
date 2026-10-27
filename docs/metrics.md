# Metrics

## Connection and world information

| Metric | Type | Meaning |
| --- | --- | --- |
| `carla_rpc_up` | Gauge | Required RPC reads and response validation succeeded in this collection: 1 or 0 |
| `carla_world_stream_connected` | Gauge | World stream TCP connection established: 1 or 0; does not prove frame progression |
| `carla_world_last_snapshot_timestamp_seconds` | Gauge | Exporter-host Unix time when a new world frame was last received |
| `carla_world_info{map,version}` | Gauge | Current map and CARLA version; value 1 |

## Simulation time and performance

| Metric | Type | Meaning |
| --- | --- | --- |
| `carla_world_frame_number` | Gauge | Last observed simulation frame number |
| `carla_world_simulation_elapsed_seconds` | Gauge | Simulation time elapsed in the current episode |
| `carla_world_frame_delta_seconds` | Gauge | Simulation time advanced by the last frame |
| `carla_world_frame_age_seconds` | Gauge | Monotonic real time since the last observed frame change |
| `carla_world_frames_per_second` | Gauge | Observed simulation frame advancement per real second in the configured window |
| `carla_world_real_time_factor` | Gauge | Observed simulation seconds advanced per real second in the configured window |
| `carla_world_frames_total` | Counter | Frame advancement accumulated by this exporter |
| `carla_world_simulation_seconds_total` | Counter | Simulation time advancement accumulated by this exporter |

## World settings

| Metric | Type | Meaning |
| --- | --- | --- |
| `carla_world_synchronous_mode` | Gauge | Synchronous mode enabled: 1 or 0 |
| `carla_world_no_rendering_mode` | Gauge | No-rendering mode enabled: 1 or 0 |
| `carla_world_fixed_delta_seconds` | Gauge | Configured fixed step; 0 for variable steps |
| `carla_world_substepping_enabled` | Gauge | Physics substepping enabled: 1 or 0 |
| `carla_world_max_substep_delta_seconds` | Gauge | Maximum configured physics substep duration |
| `carla_world_max_substeps` | Gauge | Maximum configured number of physics substeps |

## Actor counts

| Metric | Type | Meaning |
| --- | --- | --- |
| `carla_world_vehicles` | Gauge | Total vehicles in the validated snapshot, before filters |
| `carla_world_actors{kind,state}` | Gauge | Actor counts by category and lifecycle state |

## Vehicle telemetry

| Metric | Type | Meaning |
| --- | --- | --- |
| `carla_vehicle_present` | Gauge | Value 1 while the vehicle is present in the validated snapshot |
| `carla_vehicle_position_meters` | Gauge | World position by axis (`x`, `y`, `z`) |
| `carla_vehicle_yaw_radians` | Gauge | Yaw angle |
| `carla_vehicle_speed_meters_per_second` | Gauge | Magnitude of 3D velocity |
| `carla_vehicle_velocity_meters_per_second` | Gauge | World velocity components by axis (`x`, `y`, `z`) |
| `carla_vehicle_acceleration_meters_per_second_squared` | Gauge | World acceleration components by axis (`x`, `y`, `z`) |
| `carla_vehicle_angular_velocity_radians_per_second` | Gauge | Angular velocity components by axis (`x`, `y`, `z`) |
| `carla_vehicle_control_throttle_ratio` | Gauge | Last applied throttle, 0 to 1 |
| `carla_vehicle_control_brake_ratio` | Gauge | Last applied brake, 0 to 1 |
| `carla_vehicle_control_steer` | Gauge | Last applied normalized steering, -1 to 1 |
| `carla_vehicle_control_gear` | Gauge | Gear in the last applied control; negative reverse, zero neutral, positive forward |
| `carla_vehicle_control_hand_brake` | Gauge | Applied hand brake flag, 1 or 0 |
| `carla_vehicle_control_reverse` | Gauge | Applied reverse flag, 1 or 0; not measured motion direction |
| `carla_vehicle_info{role_name,blueprint_id}` | Gauge | Vehicle metadata; value 1 |

## Exporter diagnostics

| Metric | Type | Meaning |
| --- | --- | --- |
| `carla_exporter_build_info{version,revision,go_version}` | Gauge | Build information; value 1 |
| `carla_exporter_scrape_duration_seconds` | Gauge | Duration of this exporter's current CARLA collection |

## Standard collectors

| Metric | Type | Meaning |
| --- | --- | --- |
| `go_*` | Gauge / Counter / Summary | Go runtime metrics for the exporter process |
| `process_*` | Gauge / Counter | Resource usage and lifecycle metrics for the exporter process |
