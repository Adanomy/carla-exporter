package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

const (
	SensorHeaderSize  = 48
	EpisodeHeaderSize = 36
	ActorStateSize    = 119
)

type Vector [3]float64

type ActorState struct {
	ID                     uint32
	State                  uint8
	Position               Vector
	YawDegrees             float64
	Velocity               Vector
	AngularVelocityDegrees Vector
	Acceleration           Vector
	TypeData               [54]byte
}

type WorldState struct {
	EpisodeID      uint64
	Frame          uint64
	ElapsedSeconds float64
	DeltaSeconds   float64
	Actors         []ActorState
}

// ReadFrame validates the little-endian length prefix before allocating a frame.
func ReadFrame(r io.Reader, maxMessage int) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(header[:])
	if n < SensorHeaderSize+EpisodeHeaderSize || uint64(n) > uint64(maxMessage) {
		return nil, fmt.Errorf("invalid stream frame size %d (limit %d)", n, maxMessage)
	}
	frame := make([]byte, int(n))
	_, err := io.ReadFull(r, frame)
	return frame, err
}

// DecodeWorld uses explicit offsets, never native Go struct layout or unsafe.
func (p Profile) DecodeWorld(data []byte) (WorldState, error) {
	var world WorldState
	if p.version == "" {
		return world, fmt.Errorf("CARLA protocol profile is not initialized")
	}
	const header = SensorHeaderSize + EpisodeHeaderSize
	if len(data) < header || (len(data)-header)%ActorStateSize != 0 {
		return world, fmt.Errorf("invalid world snapshot length %d", len(data))
	}
	if sensor := binary.LittleEndian.Uint64(data); sensor != p.worldObserverType {
		return world, fmt.Errorf("unexpected sensor type %d for CARLA %s; expected %d", sensor, p.version, p.worldObserverType)
	}
	world.Frame = binary.LittleEndian.Uint64(data[8:])
	world.ElapsedSeconds = math.Float64frombits(binary.LittleEndian.Uint64(data[16:]))
	world.EpisodeID = binary.LittleEndian.Uint64(data[48:])
	world.DeltaSeconds = f32(data[64:])
	if !finite(world.ElapsedSeconds) || world.ElapsedSeconds < 0 || !finite(world.DeltaSeconds) || world.DeltaSeconds < 0 {
		return world, fmt.Errorf("invalid world snapshot time")
	}
	count := (len(data) - header) / ActorStateSize
	world.Actors = make([]ActorState, 0, count)
	ids := make(map[uint32]struct{}, count)
	for i := range count {
		b := data[header+i*ActorStateSize:][:ActorStateSize]
		actor := ActorState{
			ID: binary.LittleEndian.Uint32(b), State: b[4],
			Position: vector(b[5:]), YawDegrees: f32(b[21:]),
			Velocity: vector(b[29:]), AngularVelocityDegrees: vector(b[41:]), Acceleration: vector(b[53:]),
		}
		copy(actor.TypeData[:], b[65:])
		if _, duplicate := ids[actor.ID]; duplicate {
			return WorldState{}, fmt.Errorf("duplicate actor ID %d", actor.ID)
		}
		ids[actor.ID] = struct{}{}
		for _, v := range []Vector{actor.Position, actor.Velocity, actor.AngularVelocityDegrees, actor.Acceleration} {
			for _, component := range v {
				if !finite(component) {
					return WorldState{}, fmt.Errorf("non-finite actor state")
				}
			}
		}
		if !finite(actor.YawDegrees) {
			return WorldState{}, fmt.Errorf("non-finite actor yaw")
		}
		world.Actors = append(world.Actors, actor)
	}
	return world, nil
}

type VehicleControl struct {
	Throttle  float64
	Steer     float64
	Brake     float64
	HandBrake bool
	Reverse   bool
	Gear      int32
}

func DecodeVehicleControl(data [54]byte) (VehicleControl, error) {
	c := VehicleControl{
		Throttle: f32(data[:]), Steer: f32(data[4:]), Brake: f32(data[8:]),
		HandBrake: data[12] != 0, Reverse: data[13] != 0,
		Gear: int32(binary.LittleEndian.Uint32(data[15:])),
	}
	if !finite(c.Throttle) || !finite(c.Steer) || !finite(c.Brake) ||
		c.Throttle < 0 || c.Throttle > 1 || c.Brake < 0 || c.Brake > 1 || c.Steer < -1 || c.Steer > 1 ||
		data[12] > 1 || data[13] > 1 || data[14] > 1 {
		return c, fmt.Errorf("invalid vehicle control data")
	}
	return c, nil
}

func f32(b []byte) float64   { return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))) }
func vector(b []byte) Vector { return Vector{f32(b), f32(b[4:]), f32(b[8:])} }
