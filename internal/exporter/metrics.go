package exporter

import "github.com/prometheus/client_golang/prometheus"

type metricSpec struct {
	name   string
	help   string
	typeOf prometheus.ValueType
	labels []string
}

var vehicleLabels = []string{"episode_id", "actor_id"}
var axisLabels = []string{"episode_id", "actor_id", "axis"}

// The metric names are a public API. Keep labels and HELP text stable.
var metricSpecs = []metricSpec{
	{"rpc_up", "Whether the required CARLA RPC reads and response validation succeeded in this collection (1 or 0).", prometheus.GaugeValue, nil},
	{"world_stream_connected", "Whether the world state TCP connection is established; this does not prove frame progression.", prometheus.GaugeValue, nil},
	{"world_last_snapshot_timestamp_seconds", "Unix timestamp when the exporter last received a new world frame, measured on the exporter host.", prometheus.GaugeValue, nil},
	{"world_info", "Information about the current CARLA map and server version; value is always 1.", prometheus.GaugeValue, []string{"map", "version"}},
	{"world_frame_number", "Last observed simulation frame number; may reset when the world or server changes.", prometheus.GaugeValue, nil},
	{"world_simulation_elapsed_seconds", "Simulation seconds elapsed since the start of the current episode.", prometheus.GaugeValue, nil},
	{"world_frame_delta_seconds", "Simulation seconds advanced by the last frame; not wall-clock processing duration.", prometheus.GaugeValue, nil},
	{"world_frame_age_seconds", "Monotonic wall-clock seconds since the exporter last observed a new world frame.", prometheus.GaugeValue, nil},
	{"world_frames_per_second", "Observed simulation frame advancement per wall-clock second over the configured rolling window, including idle time.", prometheus.GaugeValue, nil},
	{"world_real_time_factor", "Observed simulation seconds advanced per wall-clock second over the configured rolling window, including idle time.", prometheus.GaugeValue, nil},
	{"world_frames_total", "Frame advancement accumulated by the exporter within continuously observed episodes; reconnect gaps and initial frame numbers are excluded.", prometheus.CounterValue, nil},
	{"world_simulation_seconds_total", "Simulation time advancement accumulated by the exporter within continuously observed episodes; reconnect gaps and initial elapsed time are excluded.", prometheus.CounterValue, nil},
	{"world_synchronous_mode", "Whether the current world uses synchronous mode (1 or 0).", prometheus.GaugeValue, nil},
	{"world_no_rendering_mode", "Whether rendering is disabled in the current world (1 or 0).", prometheus.GaugeValue, nil},
	{"world_fixed_delta_seconds", "Configured fixed simulation step in seconds, or 0 for a variable time step.", prometheus.GaugeValue, nil},
	{"world_substepping_enabled", "Whether physics substepping is enabled (1 or 0).", prometheus.GaugeValue, nil},
	{"world_max_substep_delta_seconds", "Configured maximum physics substep duration in simulation seconds.", prometheus.GaugeValue, nil},
	{"world_max_substeps", "Configured maximum number of physics substeps per simulation frame.", prometheus.GaugeValue, nil},
	{"world_vehicles", "Number of vehicles in the last validated world snapshot, before vehicle filters.", prometheus.GaugeValue, nil},
	{"world_actors", "Number of actors in the last validated world snapshot by blueprint category and lifecycle state.", prometheus.GaugeValue, []string{"kind", "state"}},
	{"vehicle_present", "Whether the vehicle is in the last validated snapshot; value is 1 and the series disappears after removal.", prometheus.GaugeValue, vehicleLabels},
	{"vehicle_position_meters", "Vehicle position in CARLA world coordinates in meters.", prometheus.GaugeValue, axisLabels},
	{"vehicle_yaw_radians", "Vehicle yaw in radians in CARLA's left-handed world coordinate system.", prometheus.GaugeValue, vehicleLabels},
	{"vehicle_speed_meters_per_second", "Magnitude of the vehicle's three-dimensional world velocity in meters per second.", prometheus.GaugeValue, vehicleLabels},
	{"vehicle_velocity_meters_per_second", "Vehicle world velocity component in meters per second.", prometheus.GaugeValue, axisLabels},
	{"vehicle_acceleration_meters_per_second_squared", "Vehicle world acceleration component in meters per second squared.", prometheus.GaugeValue, axisLabels},
	{"vehicle_angular_velocity_radians_per_second", "Vehicle angular velocity component in radians per second in CARLA world coordinates.", prometheus.GaugeValue, axisLabels},
	{"vehicle_control_throttle_ratio", "Last applied vehicle throttle command, in the range 0 to 1.", prometheus.GaugeValue, vehicleLabels},
	{"vehicle_control_brake_ratio", "Last applied vehicle brake command, in the range 0 to 1.", prometheus.GaugeValue, vehicleLabels},
	{"vehicle_control_steer", "Last applied normalized vehicle steering command, in the range -1 to 1; not a steering angle.", prometheus.GaugeValue, vehicleLabels},
	{"vehicle_control_gear", "Gear in the last applied vehicle control: negative for reverse, 0 for neutral, positive for forward gears.", prometheus.GaugeValue, vehicleLabels},
	{"vehicle_control_hand_brake", "Hand brake flag in the last applied vehicle control (1 or 0).", prometheus.GaugeValue, vehicleLabels},
	{"vehicle_control_reverse", "Reverse flag in the last applied vehicle control (1 or 0); not the direction of measured motion.", prometheus.GaugeValue, vehicleLabels},
	{"vehicle_info", "Vehicle role and blueprint identifier; value is always 1. Role names need not be unique.", prometheus.GaugeValue, []string{"episode_id", "actor_id", "role_name", "blueprint_id"}},
	{"exporter_build_info", "Build information for carla-exporter; value is always 1.", prometheus.GaugeValue, []string{"version", "revision", "go_version"}},
	{"exporter_scrape_duration_seconds", "Wall-clock duration of this exporter's CARLA collection in seconds.", prometheus.GaugeValue, nil},
}
