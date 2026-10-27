package protocol

import (
	"encoding/binary"
	"math"
	"slices"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

// Expectations come from upstream release sources, independently of ForVersion.
var releaseFixtures = []struct {
	version        string
	suffix         string
	observerType   uint64
	settingsFields int
}{
	{"0.9.12", "0912", 14, 10},
	{"0.9.13", "0913", 15, 10},
	{"0.9.14", "0914", 16, 10},
	{"0.9.15", "0915", 16, 11},
	{"0.9.16", "0916", 17, 11},
	{"0.10.0", "0100", 16, 11},
}

func mustProfile(t testing.TB, version string) Profile {
	t.Helper()
	profile, err := ForVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestReleaseFixtures(t *testing.T) {
	for _, release := range releaseFixtures {
		t.Run(release.version, func(t *testing.T) {
			profile := mustProfile(t, release.version)
			if profile.Version() != release.version {
				t.Fatalf("negotiated version %q, want %q", profile.Version(), release.version)
			}
			response, err := array(fixture(t, "version_response_"+release.suffix+".msgpack"), 4)
			if err != nil {
				t.Fatal(err)
			}
			data, err := unwrap(response[3])
			if err != nil {
				t.Fatal(err)
			}
			var version string
			if err := msgpack.Unmarshal(data, &version); err != nil || version != release.version {
				t.Fatalf("version response %q: %v", version, err)
			}
			settings, err := profile.DecodeSettings(fixture(t, "settings_"+release.suffix+".msgpack"))
			if err != nil || !settings.Synchronous || settings.NoRendering || !settings.Substepping || settings.FixedDeltaSeconds != 0.05 || settings.MaxSubstepDeltaSeconds != 0.01 || settings.MaxSubsteps != 10 {
				t.Fatalf("settings: %+v, %v", settings, err)
			}
			settings, err = profile.DecodeSettings(fixture(t, "settings_variable_"+release.suffix+".msgpack"))
			if err != nil || settings.Synchronous || settings.FixedDeltaSeconds != 0 || !settings.Substepping || settings.MaxSubsteps != 10 {
				t.Fatalf("variable-step settings: %+v, %v", settings, err)
			}
			frame := fixture(t, "world_"+release.suffix+".bin")
			if got := binary.LittleEndian.Uint64(frame); got != release.observerType {
				t.Fatalf("fixture observer type %d, want %d", got, release.observerType)
			}
			world, err := profile.DecodeWorld(frame)
			if err != nil {
				t.Fatal(err)
			}
			if world.EpisodeID != 987654321 || world.Frame != 120 || world.ElapsedSeconds != 6 || math.Abs(world.DeltaSeconds-0.05) > 1e-8 || len(world.Actors) != 2 {
				t.Fatalf("world: %+v", world)
			}
			vehicle := world.Actors[0]
			if vehicle.ID != 42 || vehicle.State != 1 || vehicle.Position != (Vector{1001.5, -2.25, 3}) || vehicle.YawDegrees != 90 || vehicle.Velocity != (Vector{3, 4, 0}) || vehicle.AngularVelocityDegrees != (Vector{180, -90, 45}) {
				t.Fatalf("actor layout: %+v", vehicle)
			}
			for i, want := range []float64{0.1, -0.2, 0.3} {
				if math.Abs(vehicle.Acceleration[i]-want) > 1e-7 {
					t.Fatalf("acceleration: %v", vehicle.Acceleration)
				}
			}
			control, err := DecodeVehicleControl(vehicle.TypeData)
			if err != nil || control.Gear != -1 || !control.Reverse || !control.HandBrake || control.Throttle != 0.75 || control.Brake != 0.125 || control.Steer != -0.25 {
				t.Fatalf("control layout: %+v, %v", control, err)
			}
		})
	}
}

func TestProfileRejectsMismatchedLayouts(t *testing.T) {
	for _, target := range releaseFixtures {
		for _, source := range releaseFixtures {
			t.Run(target.version+"/"+source.version, func(t *testing.T) {
				profile := mustProfile(t, target.version)
				_, err := profile.DecodeWorld(fixture(t, "world_"+source.suffix+".bin"))
				if wantError := source.observerType != target.observerType; (err != nil) != wantError {
					t.Fatalf("world source %s decoded by %s: error %v, wantError %v", source.version, target.version, err, wantError)
				}
				_, err = profile.DecodeSettings(fixture(t, "settings_"+source.suffix+".msgpack"))
				if wantError := source.settingsFields != target.settingsFields; (err != nil) != wantError {
					t.Fatalf("settings source %s decoded by %s: error %v, wantError %v", source.version, target.version, err, wantError)
				}
			})
		}
	}
}

func TestUnsupportedVersions(t *testing.T) {
	for _, version := range []string{"", "0.9.11", "0.9.17", "0.10.1", "1.0.0", "0.9.15-dev", "0.9.150", "v0.9.15", " 0.9.15", "0.9.15\n"} {
		t.Run(version, func(t *testing.T) {
			if _, err := ForVersion(version); err == nil {
				t.Fatalf("accepted unsupported version %q", version)
			}
		})
	}
	var profile Profile
	if _, err := profile.DecodeWorld(fixture(t, "world_0915.bin")); err == nil {
		t.Fatal("zero profile decoded a world frame")
	}
	if _, err := profile.DecodeSettings(fixture(t, "settings_0915.msgpack")); err == nil {
		t.Fatal("zero profile decoded settings")
	}
}

func TestSettingsRejectInvalidValues(t *testing.T) {
	cases := []struct {
		name  string
		index int
		value any
	}{
		{"optional_empty", 2, []any{}},
		{"optional_missing_value", 2, []any{true}},
		{"optional_unexpected_value", 2, []any{false, 0.05}},
		{"optional_extra_value", 2, []any{true, 0.05, 0.05}},
		{"optional_wrong_flag_type", 2, []any{1}},
		{"fixed_delta_zero", 2, []any{true, 0.0}},
		{"fixed_delta_negative", 2, []any{true, -0.05}},
		{"fixed_delta_nan", 2, []any{true, math.NaN()}},
		{"fixed_delta_infinite", 2, []any{true, math.Inf(1)}},
		{"fixed_delta_wrong_type", 2, []any{true, "0.05"}},
		{"substep_zero", 4, 0.0},
		{"substep_nan", 4, math.NaN()},
		{"substep_infinite", 4, math.Inf(1)},
		{"synchronous_wrong_type", 0, "true"},
	}
	for _, release := range releaseFixtures {
		t.Run(release.version, func(t *testing.T) {
			profile := mustProfile(t, release.version)
			valid, err := array(fixture(t, "settings_"+release.suffix+".msgpack"), release.settingsFields)
			if err != nil {
				t.Fatal(err)
			}
			for _, fields := range [][]msgpack.RawMessage{nil, valid[:len(valid)-1], append(slices.Clone(valid), []byte{0xc3})} {
				data, err := msgpack.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := profile.DecodeSettings(data); err == nil {
					t.Fatalf("accepted settings with %d fields", len(fields))
				}
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					fields := slices.Clone(valid)
					value, err := msgpack.Marshal(tc.value)
					if err != nil {
						t.Fatal(err)
					}
					fields[tc.index] = value
					data, err := msgpack.Marshal(fields)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := profile.DecodeSettings(data); err == nil {
						t.Fatal("accepted invalid settings")
					}
				})
			}
		})
	}
}
