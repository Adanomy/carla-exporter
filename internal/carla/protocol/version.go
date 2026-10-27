package protocol

import "fmt"

// Profile describes a release's wire format. Keep it bound to the connection
// that selected it: sensor registry IDs can change between CARLA releases.
type Profile struct {
	version           string
	worldObserverType uint64
	settingsFields    int
}

// ForVersion selects an explicitly supported release, never a guessed format.
func ForVersion(version string) (Profile, error) {
	switch version {
	case "0.9.12":
		return Profile{version: version, worldObserverType: 14, settingsFields: 10}, nil
	case "0.9.13":
		return Profile{version: version, worldObserverType: 15, settingsFields: 10}, nil
	case "0.9.14":
		return Profile{version: version, worldObserverType: 16, settingsFields: 10}, nil
	case "0.9.15", "0.10.0":
		return Profile{version: version, worldObserverType: 16, settingsFields: 11}, nil
	case "0.9.16":
		return Profile{version: version, worldObserverType: 17, settingsFields: 11}, nil
	default:
		return Profile{}, fmt.Errorf("unsupported CARLA version %q; supported releases: 0.9.12, 0.9.13, 0.9.14, 0.9.15, 0.9.16, 0.10.0", version)
	}
}

func (p Profile) Version() string { return p.version }
