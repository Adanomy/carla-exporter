package carla

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Adanomy/carla-exporter/internal/carla/protocol"
)

type Vector [3]float64

type VehicleControl struct {
	Throttle  float64
	Brake     float64
	Steer     float64
	Gear      int32
	HandBrake bool
	Reverse   bool
}

// Actor contains SI values in CARLA's world coordinate system.
type Actor struct {
	ID              uint32
	Kind            string
	State           string
	BlueprintID     string
	RoleName        string
	Position        Vector
	YawRadians      float64
	Velocity        Vector
	Acceleration    Vector
	AngularVelocity Vector
	Control         VehicleControl
}

type Snapshot struct {
	EpisodeID      uint64
	Frame          uint64
	ElapsedSeconds float64
	DeltaSeconds   float64
	Actors         []Actor
}

type Settings struct {
	Synchronous            bool
	NoRendering            bool
	FixedDeltaSeconds      float64
	Substepping            bool
	MaxSubstepDeltaSeconds float64
	MaxSubsteps            uint32
}

type Performance struct {
	FramesPerSecond float64
	RealTimeFactor  float64
}

// Observation also carries diagnostics when a read fails. Snapshot and Settings
// are present only when this scrape has validated the current episode via RPC.
type Observation struct {
	Version                string
	Map                    string
	Settings               *Settings
	Snapshot               *Snapshot
	StreamConnected        bool
	LastSnapshotAt         time.Time
	FrameAgeSeconds        float64
	FramesTotal            float64
	SimulationSecondsTotal float64
	Performance            *Performance
}

func convertSnapshot(raw *protocol.WorldState, descriptions map[uint32]protocol.ActorDescription) (*Snapshot, error) {
	s := &Snapshot{EpisodeID: raw.EpisodeID, Frame: raw.Frame, ElapsedSeconds: raw.ElapsedSeconds, DeltaSeconds: raw.DeltaSeconds, Actors: make([]Actor, 0, len(raw.Actors))}
	for _, source := range raw.Actors {
		description, ok := descriptions[source.ID]
		if !ok {
			// CARLA omits actors removed between the snapshot and get_actors_by_id.
			continue
		}
		a := Actor{
			ID: source.ID, Kind: actorKind(description.BlueprintID), State: actorState(source.State),
			BlueprintID: description.BlueprintID, RoleName: description.RoleName,
			Position: Vector(source.Position), YawRadians: source.YawDegrees * math.Pi / 180,
			Velocity: Vector(source.Velocity), Acceleration: Vector(source.Acceleration),
		}
		for axis, value := range source.AngularVelocityDegrees {
			a.AngularVelocity[axis] = value * math.Pi / 180
		}
		if a.Kind == "vehicle" {
			control, err := protocol.DecodeVehicleControl(source.TypeData)
			if err != nil {
				return nil, fmt.Errorf("actor %d: %w", a.ID, err)
			}
			a.Control = VehicleControl{Throttle: control.Throttle, Brake: control.Brake, Steer: control.Steer, Gear: control.Gear, HandBrake: control.HandBrake, Reverse: control.Reverse}
		}
		s.Actors = append(s.Actors, a)
	}
	return s, nil
}

func actorKind(blueprint string) string {
	switch {
	case strings.HasPrefix(blueprint, "vehicle."):
		return "vehicle"
	case strings.HasPrefix(blueprint, "walker.pedestrian."):
		return "walker"
	case strings.HasPrefix(blueprint, "sensor."):
		return "sensor"
	case blueprint == "traffic.traffic_light":
		return "traffic_light"
	case strings.HasPrefix(blueprint, "traffic."):
		return "traffic_sign"
	case strings.HasPrefix(blueprint, "controller."):
		return "controller"
	case blueprint == "spectator":
		return "spectator"
	case blueprint == "":
		return "unknown"
	default:
		return "other"
	}
}

func actorState(state uint8) string {
	switch state {
	case 0:
		return "invalid"
	case 1:
		return "active"
	case 2:
		return "dormant"
	case 3:
		return "pending_kill"
	default:
		return "unknown"
	}
}
