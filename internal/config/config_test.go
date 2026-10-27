package config

import (
	"io"
	"testing"
	"time"
)

func TestInvalidConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"--carla.address=localhost"}, {"--carla.address=:2000"}, {"--web.listen-address=:70000"},
		{"--carla.timeout=0"}, {"--performance.window=0s"}, {"--carla.max-message-bytes=-1"},
		{"--vehicle.actor-id=-1"}, {"--vehicle.blueprint=["}, {"--log.level=trace"},
		{"--log.format=invalid"}, {"--carla.reconnect-min=31s"}, {"unexpected"},
	} {
		if _, err := Parse(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestFiltersAndDefaults(t *testing.T) {
	c, err := Parse([]string{"--vehicle.actor-id=42", "--vehicle.actor-id=9", "--vehicle.role-name=hero", "--vehicle.blueprint=vehicle.tesla.*"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.PerformanceWindow != 10*time.Second || len(c.ActorIDs) != 2 || c.ActorIDs[0] != 42 || c.Roles[0] != "hero" || c.Blueprints[0] != "vehicle.tesla.*" {
		t.Fatalf("unexpected config: %+v", c)
	}
}
