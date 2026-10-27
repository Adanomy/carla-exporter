package carla

import (
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Adanomy/carla-exporter/internal/carla/protocol"
)

func TestPerformancePauseEpisodeAndReconnect(t *testing.T) {
	s := NewSession(nil, SessionOptions{PerformanceWindow: 10 * time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	s.conn = conn
	start := time.Now()
	s.observe(&protocol.WorldState{EpisodeID: 1, Frame: 1000, ElapsedSeconds: 50}, start)
	if o, _ := s.view(start.Add(9 * time.Second)); o.Performance != nil {
		t.Fatal("published performance before warm-up")
	}
	for second := 1; second <= 10; second++ {
		s.observe(&protocol.WorldState{EpisodeID: 1, Frame: 1000 + uint64(second*10), ElapsedSeconds: 50 + float64(second)/2}, start.Add(time.Duration(second)*time.Second))
	}
	o, _ := s.view(start.Add(10 * time.Second))
	if o.Performance == nil || o.Performance.FramesPerSecond != 10 || o.Performance.RealTimeFactor != 0.5 || o.FramesTotal != 100 || o.SimulationSecondsTotal != 5 {
		t.Fatalf("unexpected progression: %+v, performance %+v", o, o.Performance)
	}
	s.observe(&protocol.WorldState{EpisodeID: 1, Frame: 1100, ElapsedSeconds: 55}, start.Add(15*time.Second))
	o, _ = s.view(start.Add(20 * time.Second))
	if o.Performance == nil || o.Performance.FramesPerSecond != 0 || o.Performance.RealTimeFactor != 0 || o.FrameAgeSeconds != 10 {
		t.Fatalf("pause: %+v, performance %+v", o, o.Performance)
	}
	s.observe(&protocol.WorldState{EpisodeID: 2, Frame: 1, ElapsedSeconds: 0.05}, start.Add(21*time.Second))
	o, _ = s.view(start.Add(21 * time.Second))
	if o.Performance != nil || o.FramesTotal != 100 || o.SimulationSecondsTotal != 5 {
		t.Fatalf("episode reset changed accumulated counters: %+v", o)
	}
	// A reconnect establishes a baseline without counting unseen frames or
	// pretending the same frame changed when it was merely received again.
	s.raw, s.history = nil, nil
	s.observe(&protocol.WorldState{EpisodeID: 2, Frame: 1, ElapsedSeconds: 0.05}, start.Add(25*time.Second))
	o, _ = s.view(start.Add(25 * time.Second))
	if o.FrameAgeSeconds != 4 || o.FramesTotal != 100 {
		t.Fatalf("reconnect reset frame age or counters: %+v", o)
	}
	s.raw, s.history = nil, nil
	s.observe(&protocol.WorldState{EpisodeID: 2, Frame: 1000, ElapsedSeconds: 50}, start.Add(26*time.Second))
	o, _ = s.view(start.Add(26 * time.Second))
	if o.FramesTotal != 100 || o.SimulationSecondsTotal != 5 || o.Performance != nil {
		t.Fatalf("counted reconnect gap: %+v", o)
	}
}

func TestResumedFrameIsNotInterpolatedAcrossPause(t *testing.T) {
	s := NewSession(nil, SessionOptions{PerformanceWindow: 10 * time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	s.conn = conn
	start := time.Now()
	s.observe(&protocol.WorldState{EpisodeID: 1, Frame: 1, ElapsedSeconds: 0.05}, start)
	s.observe(&protocol.WorldState{EpisodeID: 1, Frame: 2, ElapsedSeconds: 0.1}, start.Add(100*time.Second))
	o, _ := s.view(start.Add(100 * time.Second))
	if o.Performance == nil || math.Abs(o.Performance.FramesPerSecond-0.1) > 1e-12 {
		t.Fatalf("resumed frame was spread across idle time: %+v", o.Performance)
	}
}

func TestWorldFixtureConvertedToSI(t *testing.T) {
	data, err := os.ReadFile("../../testdata/protocol/world_0915.bin")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := protocol.ForVersion("0.9.15")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := profile.DecodeWorld(data)
	if err != nil {
		t.Fatal(err)
	}
	descriptions := map[uint32]protocol.ActorDescription{42: {ID: 42, BlueprintID: "vehicle.tesla.model3", RoleName: "hero"}, 9: {ID: 9, BlueprintID: "sensor.camera.rgb"}}
	snapshot, err := convertSnapshot(&raw, descriptions)
	if err != nil {
		t.Fatal(err)
	}
	vehicle := snapshot.Actors[0]
	if vehicle.YawRadians != math.Pi/2 || vehicle.AngularVelocity != (Vector{math.Pi, -math.Pi / 2, math.Pi / 4}) || vehicle.Position[0] != 1001.5 || vehicle.Control.Gear != -1 {
		t.Fatalf("invalid SI conversion: %+v", vehicle)
	}
}

func TestSnapshotSkipsActorsWithoutDescriptions(t *testing.T) {
	raw := protocol.WorldState{
		EpisodeID: 7, Frame: 120, ElapsedSeconds: 6, DeltaSeconds: 0.05,
		Actors: []protocol.ActorState{{ID: 7}, {ID: 42, State: 1}, {ID: 9, State: 1}},
	}
	for _, tt := range []struct {
		name         string
		descriptions map[uint32]protocol.ActorDescription
		wantIDs      []uint32
	}{
		{"missing_first", map[uint32]protocol.ActorDescription{42: {ID: 42, BlueprintID: "vehicle.tesla.model3"}, 9: {ID: 9, BlueprintID: "sensor.camera.rgb"}}, []uint32{42, 9}},
		{"missing_first_and_last", map[uint32]protocol.ActorDescription{42: {ID: 42, BlueprintID: "vehicle.tesla.model3"}}, []uint32{42}},
		{"all_missing", nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := convertSnapshot(&raw, tt.descriptions)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.EpisodeID != raw.EpisodeID || snapshot.Frame != raw.Frame || snapshot.ElapsedSeconds != raw.ElapsedSeconds || snapshot.DeltaSeconds != raw.DeltaSeconds {
				t.Fatalf("missing description changed world metadata: %+v", snapshot)
			}
			if len(snapshot.Actors) != len(tt.wantIDs) {
				t.Fatalf("got actors %+v, want IDs %v", snapshot.Actors, tt.wantIDs)
			}
			for i, id := range tt.wantIDs {
				if snapshot.Actors[i].ID != id {
					t.Fatalf("got actor %d at index %d, want %d", snapshot.Actors[i].ID, i, id)
				}
			}
		})
	}
}

func TestSnapshotStillRejectsInvalidKnownVehicleControl(t *testing.T) {
	vehicle := protocol.ActorState{ID: 42}
	vehicle.TypeData[12] = 2
	raw := protocol.WorldState{Actors: []protocol.ActorState{{ID: 7}, vehicle}}
	descriptions := map[uint32]protocol.ActorDescription{42: {ID: 42, BlueprintID: "vehicle.tesla.model3"}}
	if snapshot, err := convertSnapshot(&raw, descriptions); snapshot != nil || err == nil || !strings.Contains(err.Error(), "actor 42") {
		t.Fatalf("invalid known vehicle control was not rejected: snapshot=%+v error=%v", snapshot, err)
	}
}
