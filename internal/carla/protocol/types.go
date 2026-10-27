package protocol

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"strconv"

	"github.com/vmihailenco/msgpack/v5"
)

type Token [24]byte

func (t Token) Address(fallbackHost string) (string, error) {
	if t[6] != 1 {
		return "", fmt.Errorf("unsupported stream protocol %d", t[6])
	}
	port := binary.LittleEndian.Uint16(t[4:6])
	if port == 0 {
		return "", fmt.Errorf("stream token has no port")
	}
	host := fallbackHost
	switch t[7] {
	case 0: // Partially defined endpoints use the RPC host.
	case 1:
		ip := net.IP(t[8:12])
		if !ip.IsUnspecified() {
			host = ip.String()
		}
	case 2:
		ip := net.IP(t[8:24])
		if !ip.IsUnspecified() {
			host = ip.String()
		}
	default:
		return "", fmt.Errorf("unsupported stream address type %d", t[7])
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

type EpisodeInfo struct {
	ID    uint64
	Token Token
}

func DecodeEpisode(data []byte) (EpisodeInfo, error) {
	var info EpisodeInfo
	f, err := array(data, 2)
	if err != nil {
		return info, err
	}
	if err = msgpack.Unmarshal(f[0], &info.ID); err != nil {
		return info, err
	}
	token, err := array(f[1], 1)
	if err != nil {
		return info, err
	}
	var b []byte
	if err = msgpack.Unmarshal(token[0], &b); err != nil {
		// std::array<unsigned char, 24> is represented as an integer array by
		// some MessagePack builds and as a binary object by others.
		items, arrayErr := array(token[0], 24)
		if arrayErr != nil {
			return info, arrayErr
		}
		b = make([]byte, len(items))
		for i, item := range items {
			if err := msgpack.Unmarshal(item, &b[i]); err != nil {
				return info, err
			}
		}
	}
	if len(b) != len(info.Token) {
		return info, fmt.Errorf("invalid stream token length %d", len(b))
	}
	copy(info.Token[:], b)
	return info, nil
}

type Settings struct {
	Synchronous            bool
	NoRendering            bool
	FixedDeltaSeconds      float64
	Substepping            bool
	MaxSubstepDeltaSeconds float64
	MaxSubsteps            uint32
}

func (p Profile) DecodeSettings(data []byte) (Settings, error) {
	var settings Settings
	if p.version == "" {
		return settings, fmt.Errorf("CARLA protocol profile is not initialized")
	}
	f, err := array(data, p.settingsFields)
	if err != nil {
		return settings, err
	}
	targets := []struct {
		index  int
		target any
	}{
		{0, &settings.Synchronous}, {1, &settings.NoRendering},
		{3, &settings.Substepping}, {4, &settings.MaxSubstepDeltaSeconds}, {5, &settings.MaxSubsteps},
	}
	for _, target := range targets {
		if err := msgpack.Unmarshal(f[target.index], target.target); err != nil {
			return settings, err
		}
	}
	optional, err := array(f[2], -1)
	if err != nil {
		return settings, err
	}
	if len(optional) != 1 && len(optional) != 2 {
		return settings, fmt.Errorf("invalid fixed delta optional")
	}
	var present bool
	if err := msgpack.Unmarshal(optional[0], &present); err != nil {
		return settings, err
	}
	if present != (len(optional) == 2) {
		return settings, fmt.Errorf("inconsistent fixed delta optional")
	}
	if present {
		if err := msgpack.Unmarshal(optional[1], &settings.FixedDeltaSeconds); err != nil {
			return settings, err
		}
		if settings.FixedDeltaSeconds <= 0 {
			return settings, fmt.Errorf("fixed delta must be positive")
		}
	}
	if !finite(settings.FixedDeltaSeconds) || !finite(settings.MaxSubstepDeltaSeconds) || settings.MaxSubstepDeltaSeconds <= 0 {
		return settings, fmt.Errorf("invalid simulation time settings")
	}
	return settings, nil
}

func DecodeMap(data []byte) (string, error) {
	f, err := array(data, 2)
	if err != nil {
		return "", err
	}
	var name string
	if err := msgpack.Unmarshal(f[0], &name); err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("empty CARLA map name")
	}
	return name, nil
}

type ActorDescription struct {
	ID          uint32
	BlueprintID string
	RoleName    string
}

func DecodeActors(data []byte) ([]ActorDescription, error) {
	actors, err := array(data, -1)
	if err != nil {
		return nil, err
	}
	result := make([]ActorDescription, 0, len(actors))
	for _, raw := range actors {
		f, err := array(raw, 6)
		if err != nil {
			return nil, err
		}
		var actor ActorDescription
		if err := msgpack.Unmarshal(f[0], &actor.ID); err != nil {
			return nil, err
		}
		description, err := array(f[2], 3)
		if err != nil {
			return nil, err
		}
		if err := msgpack.Unmarshal(description[1], &actor.BlueprintID); err != nil {
			return nil, err
		}
		attrs, err := array(description[2], -1)
		if err != nil {
			return nil, err
		}
		for _, rawAttr := range attrs {
			attr, err := array(rawAttr, 3)
			if err != nil {
				return nil, err
			}
			var key string
			if err := msgpack.Unmarshal(attr[0], &key); err != nil {
				return nil, err
			}
			if key == "role_name" {
				if err := msgpack.Unmarshal(attr[2], &actor.RoleName); err != nil {
					return nil, err
				}
			}
		}
		result = append(result, actor)
	}
	return result, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
