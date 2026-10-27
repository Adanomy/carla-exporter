package exporter

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Adanomy/carla-exporter/internal/carla"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

type stubReader struct {
	observation carla.Observation
	err         error
}

func (s *stubReader) Read(context.Context) (carla.Observation, error) { return s.observation, s.err }

func completeObservation() carla.Observation {
	return carla.Observation{
		Version: "0.9.15", Map: "Carla/Maps/Town01", StreamConnected: true,
		LastSnapshotAt: time.Unix(1700000000, 0), FrameAgeSeconds: 2,
		FramesTotal: 200, SimulationSecondsTotal: 10,
		Settings:    &carla.Settings{Synchronous: true, FixedDeltaSeconds: 0.05, Substepping: true, MaxSubstepDeltaSeconds: 0.01, MaxSubsteps: 10},
		Performance: &carla.Performance{FramesPerSecond: 20, RealTimeFactor: 1},
		Snapshot: &carla.Snapshot{EpisodeID: 100, Frame: 300, ElapsedSeconds: 15, DeltaSeconds: 0.05, Actors: []carla.Actor{
			{ID: 42, Kind: "vehicle", State: "active", BlueprintID: "vehicle.tesla.model3", RoleName: "hero", YawRadians: math.Pi / 2, Velocity: carla.Vector{3, 4, 0}, AngularVelocity: carla.Vector{0, 0, math.Pi}, Control: carla.VehicleControl{Throttle: 0.75, Brake: 0.125, Steer: -0.25, Gear: -1, Reverse: true}},
			{ID: 99, Kind: "vehicle", State: "dormant", BlueprintID: "vehicle.other", RoleName: "npc"},
			{ID: 1, Kind: "sensor", State: "active"},
		}},
	}
}

func registryFor(t *testing.T, source Reader, options Options) *prometheus.Registry {
	t.Helper()
	options.Timeout = time.Second
	r := prometheus.NewPedanticRegistry()
	r.MustRegister(NewCollector(t.Context(), source, options, BuildInfo{"dev", "test", "go1.27.3"}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	return r
}

func families(t *testing.T, r *prometheus.Registry) map[string]*dto.MetricFamily {
	t.Helper()
	all, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]*dto.MetricFamily, len(all))
	for _, family := range all {
		result[family.GetName()] = family
	}
	return result
}

func TestApprovedMetricContract(t *testing.T) {
	source := &stubReader{observation: completeObservation()}
	r := registryFor(t, source, Options{})
	all := families(t, r)
	// Independent contract list: do not derive this from metricSpecs.
	const approved = `rpc_up world_stream_connected world_last_snapshot_timestamp_seconds world_info
world_frame_number world_simulation_elapsed_seconds world_frame_delta_seconds world_frame_age_seconds
world_frames_per_second world_real_time_factor world_frames_total world_simulation_seconds_total
world_synchronous_mode world_no_rendering_mode world_fixed_delta_seconds world_substepping_enabled
world_max_substep_delta_seconds world_max_substeps world_vehicles world_actors
vehicle_present vehicle_position_meters vehicle_yaw_radians vehicle_speed_meters_per_second
vehicle_velocity_meters_per_second vehicle_acceleration_meters_per_second_squared
vehicle_angular_velocity_radians_per_second vehicle_control_throttle_ratio vehicle_control_brake_ratio
vehicle_control_steer vehicle_control_gear vehicle_control_hand_brake vehicle_control_reverse vehicle_info
exporter_build_info exporter_scrape_duration_seconds`
	names := strings.Fields(approved)
	if len(all) != 36 || len(names) != 36 {
		t.Fatalf("expected exactly 36 metric families, got %d", len(all))
	}
	for _, name := range names {
		family, ok := all["carla_"+name]
		if !ok {
			t.Errorf("missing metric carla_%s", name)
			continue
		}
		want := dto.MetricType_GAUGE
		if strings.HasSuffix(name, "_total") {
			want = dto.MetricType_COUNTER
		}
		if family.GetType() != want {
			t.Errorf("%s has type %s, want %s", name, family.GetType(), want)
		}
	}
	if problems, err := testutil.GatherAndLint(r); err != nil || len(problems) != 0 {
		t.Fatalf("metric lint: %v, %v", problems, err)
	}
	if got := all["carla_vehicle_speed_meters_per_second"].Metric[0].GetGauge().GetValue(); got != 5 {
		t.Fatalf("3D speed = %v, want 5", got)
	}
	if got := len(all["carla_world_actors"].Metric); got != 45 {
		t.Fatalf("expected 45 bounded actor category series, got %d", got)
	}
}

func TestFiltersDoNotChangeWorldCounts(t *testing.T) {
	r := registryFor(t, &stubReader{observation: completeObservation()}, Options{ActorIDs: []uint32{42}, Roles: []string{"hero"}, Blueprints: []string{"vehicle.tesla.*"}})
	all := families(t, r)
	if len(all["carla_vehicle_present"].Metric) != 1 || all["carla_world_vehicles"].Metric[0].GetGauge().GetValue() != 2 {
		t.Fatal("vehicle filter changed world counts or did not filter vehicle series")
	}
	for _, label := range all["carla_vehicle_info"].Metric[0].Label {
		if label.GetName() == "blueprint_id" && label.GetValue() != "vehicle.tesla.model3" {
			t.Fatal("unexpected blueprint label")
		}
	}
}

func TestFailureAndDisappearanceDoNotPublishStaleValues(t *testing.T) {
	source := &stubReader{observation: completeObservation()}
	r := registryFor(t, source, Options{})
	_ = families(t, r)
	source.observation.Snapshot = &carla.Snapshot{EpisodeID: 100}
	all := families(t, r)
	if _, ok := all["carla_vehicle_present"]; ok {
		t.Fatal("removed vehicles are still published")
	}
	if all["carla_world_vehicles"].Metric[0].GetGauge().GetValue() != 0 {
		t.Fatal("empty world must publish a zero vehicle count")
	}
	source.observation = completeObservation()
	source.err = errors.New("RPC unavailable")
	all = families(t, r)
	for _, name := range []string{"carla_vehicle_present", "carla_world_frame_number", "carla_world_info", "carla_world_vehicles", "carla_world_frames_per_second"} {
		if _, ok := all[name]; ok {
			t.Errorf("published stale %s after RPC failure", name)
		}
	}
	if all["carla_rpc_up"].Metric[0].GetGauge().GetValue() != 0 || all["carla_world_frames_total"].Metric[0].GetCounter().GetValue() != 200 {
		t.Fatal("failure lost diagnostics")
	}
}

func TestConcurrentScrapesAndHealth(t *testing.T) {
	r := registryFor(t, &stubReader{observation: completeObservation()}, Options{})
	handler := NewHandler(r, time.Second)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
			if response.Code != 200 && response.Code != 503 {
				t.Errorf("unexpected concurrent scrape status %d", response.Code)
			}
		})
	}
	workers.Wait()
	for _, url := range []string{"/", "/healthz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", url, nil))
		if response.Code != 200 {
			t.Fatalf("%s returned %d", url, response.Code)
		}
	}
}
