// Package exporter translates validated CARLA observations into Prometheus metrics.
package exporter

import (
	"context"
	"log/slog"
	"math"
	"path"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/Adanomy/carla-exporter/internal/carla"
	"github.com/prometheus/client_golang/prometheus"
)

// Reader is owned by the collector, allowing deterministic tests without CARLA.
type Reader interface {
	Read(context.Context) (carla.Observation, error)
}

type BuildInfo struct {
	Version   string
	Revision  string
	GoVersion string
}

type Options struct {
	Timeout    time.Duration
	ActorIDs   []uint32
	Roles      []string
	Blueprints []string
}

type Collector struct {
	ctx          context.Context
	source       Reader
	options      Options
	build        BuildInfo
	logger       *slog.Logger
	descriptions map[string]*prometheus.Desc
	mu           sync.Mutex
	lastError    string
}

func NewCollector(ctx context.Context, source Reader, options Options, build BuildInfo, logger *slog.Logger) *Collector {
	c := &Collector{ctx: ctx, source: source, options: options, build: build, logger: logger, descriptions: make(map[string]*prometheus.Desc, len(metricSpecs))}
	for _, metric := range metricSpecs {
		c.descriptions[metric.name] = prometheus.NewDesc("carla_"+metric.name, metric.help, metric.labels, nil)
	}
	return c
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, metric := range metricSpecs {
		ch <- c.descriptions[metric.name]
	}
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(c.ctx, c.options.Timeout)
	defer cancel()
	o, err := c.source.Read(ctx)
	c.logResult(err)
	emit := func(name string, value float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(c.descriptions[name], prometheus.GaugeValue, value, labels...)
	}
	emit("exporter_build_info", 1, c.build.Version, c.build.Revision, c.build.GoVersion)
	emit("rpc_up", boolean(err == nil))
	emit("world_stream_connected", boolean(o.StreamConnected))
	ch <- prometheus.MustNewConstMetric(c.descriptions["world_frames_total"], prometheus.CounterValue, o.FramesTotal)
	ch <- prometheus.MustNewConstMetric(c.descriptions["world_simulation_seconds_total"], prometheus.CounterValue, o.SimulationSecondsTotal)
	if !o.LastSnapshotAt.IsZero() {
		emit("world_last_snapshot_timestamp_seconds", float64(o.LastSnapshotAt.UnixNano())/1e9)
		emit("world_frame_age_seconds", o.FrameAgeSeconds)
	}
	if err == nil {
		if o.Settings != nil {
			emit("world_info", 1, o.Map, o.Version)
			emit("world_synchronous_mode", boolean(o.Settings.Synchronous))
			emit("world_no_rendering_mode", boolean(o.Settings.NoRendering))
			emit("world_fixed_delta_seconds", o.Settings.FixedDeltaSeconds)
			emit("world_substepping_enabled", boolean(o.Settings.Substepping))
			emit("world_max_substep_delta_seconds", o.Settings.MaxSubstepDeltaSeconds)
			emit("world_max_substeps", float64(o.Settings.MaxSubsteps))
		}
		if o.Snapshot != nil && o.StreamConnected {
			s := o.Snapshot
			emit("world_frame_number", float64(s.Frame))
			emit("world_simulation_elapsed_seconds", s.ElapsedSeconds)
			emit("world_frame_delta_seconds", s.DeltaSeconds)
			if o.Performance != nil {
				emit("world_frames_per_second", o.Performance.FramesPerSecond)
				emit("world_real_time_factor", o.Performance.RealTimeFactor)
			}
			c.collectActors(s, emit)
		}
	}
	emit("exporter_scrape_duration_seconds", time.Since(started).Seconds())
}

func (c *Collector) logResult(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if message := err.Error(); message != c.lastError {
			c.logger.Warn("CARLA collection failed", "error", err)
			c.lastError = message
		}
	} else if c.lastError != "" {
		c.logger.Info("CARLA collection recovered")
		c.lastError = ""
	}
}

func (c *Collector) collectActors(s *carla.Snapshot, emit func(string, float64, ...string)) {
	kinds := []string{"vehicle", "walker", "sensor", "traffic_light", "traffic_sign", "controller", "spectator", "other", "unknown"}
	states := []string{"active", "dormant", "pending_kill", "invalid", "unknown"}
	counts := make(map[[2]string]int)
	vehicles := 0
	episode := strconv.FormatUint(s.EpisodeID, 10)
	for _, actor := range s.Actors {
		counts[[2]string{actor.Kind, actor.State}]++
		if actor.Kind != "vehicle" {
			continue
		}
		vehicles++
		if !c.include(actor) {
			continue
		}
		id := strconv.FormatUint(uint64(actor.ID), 10)
		labels := []string{episode, id}
		emit("vehicle_present", 1, labels...)
		emit("vehicle_info", 1, episode, id, actor.RoleName, actor.BlueprintID)
		emit("vehicle_yaw_radians", actor.YawRadians, labels...)
		emit("vehicle_speed_meters_per_second", math.Hypot(math.Hypot(actor.Velocity[0], actor.Velocity[1]), actor.Velocity[2]), labels...)
		for axis, name := range []string{"x", "y", "z"} {
			emit("vehicle_position_meters", actor.Position[axis], episode, id, name)
			emit("vehicle_velocity_meters_per_second", actor.Velocity[axis], episode, id, name)
			emit("vehicle_acceleration_meters_per_second_squared", actor.Acceleration[axis], episode, id, name)
			emit("vehicle_angular_velocity_radians_per_second", actor.AngularVelocity[axis], episode, id, name)
		}
		emit("vehicle_control_throttle_ratio", actor.Control.Throttle, labels...)
		emit("vehicle_control_brake_ratio", actor.Control.Brake, labels...)
		emit("vehicle_control_steer", actor.Control.Steer, labels...)
		emit("vehicle_control_gear", float64(actor.Control.Gear), labels...)
		emit("vehicle_control_hand_brake", boolean(actor.Control.HandBrake), labels...)
		emit("vehicle_control_reverse", boolean(actor.Control.Reverse), labels...)
	}
	emit("world_vehicles", float64(vehicles))
	for _, kind := range kinds {
		for _, state := range states {
			emit("world_actors", float64(counts[[2]string{kind, state}]), kind, state)
		}
	}
}

func (c *Collector) include(actor carla.Actor) bool {
	if len(c.options.ActorIDs) > 0 && !slices.Contains(c.options.ActorIDs, actor.ID) {
		return false
	}
	if len(c.options.Roles) > 0 && !slices.Contains(c.options.Roles, actor.RoleName) {
		return false
	}
	if len(c.options.Blueprints) > 0 {
		for _, pattern := range c.options.Blueprints {
			if matches, _ := path.Match(pattern, actor.BlueprintID); matches {
				return true
			}
		}
		return false
	}
	return true
}

func boolean(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
