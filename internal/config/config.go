// Package config parses and validates command-line configuration.
package config

import (
	"flag"
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddress     string
	CARLAAddress      string
	StreamAddress     string
	Timeout           time.Duration
	PerformanceWindow time.Duration
	ReconnectMin      time.Duration
	ReconnectMax      time.Duration
	MaxMessageBytes   int
	ActorIDs          []uint32
	Roles             []string
	Blueprints        []string
	LogLevel          string
	LogFormat         string
	Version           bool
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	if value == "" {
		return fmt.Errorf("filter value must not be empty")
	}
	*s = append(*s, value)
	return nil
}

func Parse(args []string, output io.Writer) (Config, error) {
	var c Config
	var ids, roles, blueprints stringList
	f := flag.NewFlagSet("carla-exporter", flag.ContinueOnError)
	f.SetOutput(output)
	f.StringVar(&c.ListenAddress, "web.listen-address", ":9840", "HTTP listen address.")
	f.StringVar(&c.CARLAAddress, "carla.address", "localhost:2000", "CARLA RPC host:port.")
	f.StringVar(&c.StreamAddress, "carla.stream-address", "", "Optional world stream host:port override for NAT or port forwarding.")
	f.DurationVar(&c.Timeout, "carla.timeout", 5*time.Second, "Total RPC budget per collection and stream setup attempt.")
	f.DurationVar(&c.PerformanceWindow, "performance.window", 10*time.Second, "Rolling window for simulation FPS and real-time factor.")
	f.DurationVar(&c.ReconnectMin, "carla.reconnect-min", time.Second, "Initial stream reconnect delay.")
	f.DurationVar(&c.ReconnectMax, "carla.reconnect-max", 30*time.Second, "Maximum base stream reconnect delay, before jitter.")
	f.IntVar(&c.MaxMessageBytes, "carla.max-message-bytes", 16<<20, "Maximum RPC response or stream frame size in bytes.")
	f.Var(&ids, "vehicle.actor-id", "Vehicle actor ID to include; repeat to include multiple IDs.")
	f.Var(&roles, "vehicle.role-name", "Vehicle role to include; repeat to include multiple roles.")
	f.Var(&blueprints, "vehicle.blueprint", "Vehicle blueprint glob to include; repeat for multiple patterns.")
	f.StringVar(&c.LogLevel, "log.level", "info", "Log level: debug, info, warn, or error.")
	f.StringVar(&c.LogFormat, "log.format", "text", "Log format: text or json.")
	f.BoolVar(&c.Version, "version", false, "Print build information and exit.")
	if err := f.Parse(args); err != nil {
		return c, err
	}
	if f.NArg() != 0 {
		return c, fmt.Errorf("unexpected positional arguments: %s", strings.Join(f.Args(), " "))
	}
	if c.Version {
		return c, nil
	}
	for name, address := range map[string]string{"web.listen-address": c.ListenAddress, "carla.address": c.CARLAAddress, "carla.stream-address": c.StreamAddress} {
		if name == "carla.stream-address" && address == "" {
			continue
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return c, fmt.Errorf("invalid %s: %w", name, err)
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || (name != "web.listen-address" && host == "") {
			return c, fmt.Errorf("invalid %s: expected host and port in range 1..65535", name)
		}
	}
	if c.Timeout <= 0 || c.Timeout > time.Minute {
		return c, fmt.Errorf("carla.timeout must be greater than zero and at most 1m")
	}
	if c.PerformanceWindow < time.Second || c.PerformanceWindow > 15*time.Minute {
		return c, fmt.Errorf("performance.window must be between 1s and 15m")
	}
	if c.ReconnectMin <= 0 || c.ReconnectMax < c.ReconnectMin || c.ReconnectMax > 5*time.Minute {
		return c, fmt.Errorf("invalid CARLA reconnect delays")
	}
	if c.MaxMessageBytes < 1024 || c.MaxMessageBytes > 64<<20 {
		return c, fmt.Errorf("carla.max-message-bytes must be between 1024 and 67108864")
	}
	for _, id := range ids {
		n, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return c, fmt.Errorf("invalid vehicle.actor-id %q: %w", id, err)
		}
		c.ActorIDs = append(c.ActorIDs, uint32(n))
	}
	for _, pattern := range blueprints {
		if _, err := path.Match(pattern, ""); err != nil {
			return c, fmt.Errorf("invalid vehicle.blueprint %q: %w", pattern, err)
		}
	}
	c.Roles, c.Blueprints = roles, blueprints
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return c, fmt.Errorf("invalid log.level %q", c.LogLevel)
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		return c, fmt.Errorf("invalid log.format %q", c.LogFormat)
	}
	return c, nil
}
