package integration

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Adanomy/carla-exporter/internal/carla"
	"github.com/Adanomy/carla-exporter/internal/exporter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmihailenco/msgpack/v5"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../testdata/protocol", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wireServer is deliberately independent of the production protocol package.
// It uses the upstream RPC shapes and Python-packed binary fixtures.
type wireServer struct {
	t                *testing.T
	rpc              net.Listener
	stream           net.Listener
	workers          sync.WaitGroup
	mu               sync.Mutex
	connections      map[net.Conn]bool
	streams          map[net.Conn]*sync.Mutex
	closed           bool
	frame            []byte
	version          string
	settings         []byte
	episode          uint64
	changeDuringRead bool
	omittedActorID   uint32
	subscriptions    uint64
}

func newWireServer(t *testing.T, version string) *wireServer {
	t.Helper()
	rpc, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		rpc.Close()
		t.Fatal(err)
	}
	s := &wireServer{t: t, rpc: rpc, stream: stream, connections: make(map[net.Conn]bool), streams: make(map[net.Conn]*sync.Mutex), frame: loadFixture(t, "world_"+fixtureSuffix(version)+".bin"), version: version, settings: loadFixture(t, "settings_"+fixtureSuffix(version)+".msgpack"), episode: 987654321}
	s.workers.Go(func() { s.accept(rpc, s.handleRPC) })
	s.workers.Go(func() { s.accept(stream, s.handleStream) })
	t.Cleanup(s.close)
	return s
}

func (s *wireServer) accept(listener net.Listener, handle func(net.Conn)) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.connections[conn] = true
		s.mu.Unlock()
		s.workers.Go(func() {
			defer conn.Close()
			defer func() { s.mu.Lock(); delete(s.connections, conn); delete(s.streams, conn); s.mu.Unlock() }()
			handle(conn)
		})
	}
}

func (s *wireServer) handleRPC(conn net.Conn) {
	decoder := msgpack.NewDecoder(conn)
	encoder := msgpack.NewEncoder(conn)
	for {
		var request []msgpack.RawMessage
		if err := decoder.Decode(&request); err != nil {
			return
		}
		if len(request) != 4 {
			s.t.Error("RPC request is not a four-element array")
			return
		}
		var kind, id uint32
		var method string
		var params []msgpack.RawMessage
		if err := msgpack.Unmarshal(request[0], &kind); err != nil {
			s.t.Error(err)
			return
		}
		if err := msgpack.Unmarshal(request[1], &id); err != nil {
			s.t.Error(err)
			return
		}
		if err := msgpack.Unmarshal(request[2], &method); err != nil {
			s.t.Error(err)
			return
		}
		if err := msgpack.Unmarshal(request[3], &params); err != nil {
			s.t.Error(err)
			return
		}
		if kind != 0 || len(params) < 1 {
			s.t.Error("invalid RPC request header")
			return
		}
		var metadata []bool
		if err := msgpack.Unmarshal(params[0], &metadata); err != nil || len(metadata) != 1 || metadata[0] {
			s.t.Error("missing synchronous CARLA metadata")
			return
		}
		s.mu.Lock()
		version, episode, settings, omittedActorID := s.version, s.episode, s.settings, s.omittedActorID
		if method == "get_episode_settings" && s.changeDuringRead {
			s.episode++
			s.changeDuringRead = false
		}
		s.mu.Unlock()
		var result any
		switch method {
		case "version":
			result = version
		case "get_episode_info":
			var token [24]byte
			binary.LittleEndian.PutUint32(token[:], 7)
			binary.LittleEndian.PutUint16(token[4:], uint16(s.stream.Addr().(*net.TCPAddr).Port))
			token[6] = 1
			// Use the integer-array token representation of the C++ SDK.
			bytesAsInts := make([]uint32, 24)
			for i, b := range token {
				bytesAsInts[i] = uint32(b)
			}
			result = []any{episode, []any{bytesAsInts}}
		case "get_map_info":
			result = msgpack.RawMessage(loadFixture(s.t, "map_0915.msgpack"))
		case "get_episode_settings":
			result = msgpack.RawMessage(settings)
		case "get_actors_by_id":
			if len(params) != 2 {
				s.t.Error("missing actor IDs")
				return
			}
			var ids []uint32
			if err := msgpack.Unmarshal(params[1], &ids); err != nil {
				s.t.Error(err)
				return
			}
			var actors []msgpack.RawMessage
			if err := msgpack.Unmarshal(loadFixture(s.t, "actors_0915.msgpack"), &actors); err != nil {
				s.t.Error(err)
				return
			}
			selected := make([]msgpack.RawMessage, 0)
			for _, actor := range actors {
				var fields []msgpack.RawMessage
				_ = msgpack.Unmarshal(actor, &fields)
				var actorID uint32
				_ = msgpack.Unmarshal(fields[0], &actorID)
				for _, wanted := range ids {
					if actorID == wanted && actorID != omittedActorID {
						selected = append(selected, actor)
					}
				}
			}
			result = selected
		default:
			s.t.Errorf("unexpected or mutating RPC method %q", method)
			return
		}
		if err := encoder.Encode([]any{1, id, nil, []any{[]any{1, result}}}); err != nil {
			return
		}
	}
}

func writeFrame(conn net.Conn, frame []byte) error {
	var length [4]byte
	binary.LittleEndian.PutUint32(length[:], uint32(len(frame)))
	if _, err := conn.Write(length[:]); err != nil {
		return err
	}
	_, err := conn.Write(frame)
	return err
}

func (s *wireServer) handleStream(conn net.Conn) {
	var subscription [4]byte
	if _, err := io.ReadFull(conn, subscription[:]); err != nil {
		return
	}
	if binary.LittleEndian.Uint32(subscription[:]) != 7 {
		s.t.Error("invalid stream subscription")
		return
	}
	lock := new(sync.Mutex)
	lock.Lock()
	s.mu.Lock()
	s.streams[conn] = lock
	s.subscriptions++
	frame := append([]byte(nil), s.frame...)
	s.mu.Unlock()
	err := writeFrame(conn, frame)
	lock.Unlock()
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, conn)
}

func (s *wireServer) send(frame []byte) {
	s.mu.Lock()
	s.frame = append([]byte(nil), frame...)
	streams := make(map[net.Conn]*sync.Mutex)
	for conn, lock := range s.streams {
		streams[conn] = lock
	}
	s.mu.Unlock()
	for conn, lock := range streams {
		lock.Lock()
		_ = writeFrame(conn, frame)
		lock.Unlock()
	}
}

func (s *wireServer) disconnectStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.streams {
		_ = conn.Close()
	}
}

func (s *wireServer) restart(version string, episode uint64) {
	frame := loadFixture(s.t, "world_"+fixtureSuffix(version)+".bin")
	settings := loadFixture(s.t, "settings_"+fixtureSuffix(version)+".msgpack")
	binary.LittleEndian.PutUint64(frame[48:], episode)
	binary.LittleEndian.PutUint64(frame[8:], 1)
	binary.LittleEndian.PutUint64(frame[16:], math.Float64bits(0.05))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version, s.settings, s.frame, s.episode = version, settings, frame, episode
	// Keep the endpoint addresses, as when replacing CARLA on the same host.
	for conn := range s.connections {
		_ = conn.Close()
	}
}

func (s *wireServer) close() {
	s.mu.Lock()
	s.closed = true
	_ = s.rpc.Close()
	_ = s.stream.Close()
	for conn := range s.connections {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.workers.Wait()
}

func startExporter(t *testing.T, address, streamAddress string, timeout time.Duration) *httptest.Server {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := carla.NewClient(address, 1<<20)
	session := carla.NewSession(client, carla.SessionOptions{Address: address, StreamAddress: streamAddress, Timeout: timeout, PerformanceWindow: time.Second, ReconnectMin: 10 * time.Millisecond, ReconnectMax: 100 * time.Millisecond, MaxMessageBytes: 1 << 20}, logger)
	done := make(chan struct{})
	go func() { defer close(done); session.Run(ctx) }()
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(exporter.NewCollector(ctx, session, exporter.Options{Timeout: timeout}, exporter.BuildInfo{Version: "test", Revision: "test", GoVersion: "test"}, logger))
	server := httptest.NewServer(exporter.NewHandler(registry, timeout))
	t.Cleanup(func() { cancel(); server.Close(); <-done; client.Close() })
	return server
}

func scrape(t *testing.T, server *httptest.Server) string {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), "GET", server.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	b, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("scrape returned %d: %s", response.StatusCode, b)
	}
	return string(b)
}

func awaitMetrics(t *testing.T, server *httptest.Server, check func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = scrape(t, server)
		if check(last) {
			return last
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("metrics did not reach expected state:\n%s", last)
	return ""
}

func fixtureSuffix(version string) string {
	return strings.ReplaceAll(version, ".", "")
}

func TestExporterEndToEnd(t *testing.T) {
	for _, version := range []string{"0.9.12", "0.9.13", "0.9.14", "0.9.15", "0.9.16", "0.10.0"} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			testExporterVersion(t, version)
		})
	}
}

func TestExporterMissingActorDescriptionDuringPause(t *testing.T) {
	for _, version := range []string{"0.9.12", "0.9.13", "0.9.14", "0.9.15", "0.9.16", "0.10.0"} {
		t.Run(version, func(t *testing.T) {
			for _, missing := range []struct {
				name string
				id   uint32
			}{
				{name: "sensor", id: 9},
				{name: "vehicle", id: 42},
			} {
				t.Run(missing.name, func(t *testing.T) {
					t.Parallel()
					wire := newWireServer(t, version)
					wire.mu.Lock()
					wire.omittedActorID = missing.id
					wire.mu.Unlock()
					server := startExporter(t, wire.rpc.Addr().String(), "", time.Second)
					awaitMetrics(t, server, func(metrics string) bool {
						return strings.Contains(metrics, "\ncarla_world_last_snapshot_timestamp_seconds ")
					})
					// Keep the initial frame paused while RPC omits an actor removed
					// before its description could be cached by the first scrape.
					for attempt := range 3 {
						metrics := scrape(t, server)
						for _, expected := range []string{
							"carla_rpc_up 1\n",
							"carla_world_stream_connected 1\n",
							"carla_world_frame_number 120\n",
							"carla_world_simulation_elapsed_seconds 6\n",
							"carla_world_synchronous_mode 1\n",
							"carla_world_fixed_delta_seconds 0.05\n",
							fmt.Sprintf(`carla_world_info{map="Carla/Maps/Town01",version="%s"} 1`, version),
						} {
							if !strings.Contains(metrics, expected) {
								t.Errorf("scrape %d lost %q with actor %d absent from RPC descriptions", attempt+1, expected, missing.id)
							}
						}
						if missing.id == 9 {
							for _, expected := range []string{
								"carla_world_vehicles 1\n",
								`carla_vehicle_speed_meters_per_second{actor_id="42",episode_id="987654321"} 5`,
								`carla_vehicle_position_meters{actor_id="42",axis="x",episode_id="987654321"} 1001.5`,
							} {
								if !strings.Contains(metrics, expected) {
									t.Errorf("scrape %d lost valid vehicle telemetry: %q", attempt+1, expected)
								}
							}
						} else if strings.Contains(metrics, "\ncarla_vehicle_present{") || !strings.Contains(metrics, "carla_world_vehicles 0\n") {
							t.Errorf("scrape %d published vehicle telemetry without a description", attempt+1)
						}
					}
					// A later RPC response can resolve the same paused snapshot.
					wire.mu.Lock()
					wire.omittedActorID = 0
					wire.mu.Unlock()
					metrics := scrape(t, server)
					for _, expected := range []string{
						"carla_rpc_up 1\n",
						"carla_world_frame_number 120\n",
						"carla_world_vehicles 1\n",
						`carla_world_actors{kind="sensor",state="active"} 1`,
						`carla_vehicle_present{actor_id="42",episode_id="987654321"} 1`,
						`carla_vehicle_speed_meters_per_second{actor_id="42",episode_id="987654321"} 5`,
					} {
						if !strings.Contains(metrics, expected) {
							t.Errorf("actor description did not recover without a tick: %q", expected)
						}
					}
				})
			}
		})
	}
}

func testExporterVersion(t *testing.T, version string) {
	t.Helper()
	wire := newWireServer(t, version)
	server := startExporter(t, wire.rpc.Addr().String(), "", time.Second)
	metrics := awaitMetrics(t, server, func(s string) bool { return strings.Contains(s, "carla_world_frame_number 120\n") })
	for _, expected := range []string{
		"carla_rpc_up 1\n", "carla_world_vehicles 1\n",
		fmt.Sprintf(`carla_world_info{map="Carla/Maps/Town01",version="%s"} 1`, version),
		"carla_world_simulation_elapsed_seconds 6\n",
		"carla_world_synchronous_mode 1\n", "carla_world_no_rendering_mode 0\n",
		"carla_world_fixed_delta_seconds 0.05\n", "carla_world_substepping_enabled 1\n",
		"carla_world_max_substep_delta_seconds 0.01\n", "carla_world_max_substeps 10\n",
		`carla_world_actors{kind="vehicle",state="active"} 1`,
		`carla_world_actors{kind="sensor",state="active"} 1`,
		`carla_vehicle_present{actor_id="42",episode_id="987654321"} 1`,
		`carla_vehicle_position_meters{actor_id="42",axis="x",episode_id="987654321"} 1001.5`,
		`carla_vehicle_position_meters{actor_id="42",axis="y",episode_id="987654321"} -2.25`,
		`carla_vehicle_position_meters{actor_id="42",axis="z",episode_id="987654321"} 3`,
		`carla_vehicle_velocity_meters_per_second{actor_id="42",axis="x",episode_id="987654321"} 3`,
		`carla_vehicle_acceleration_meters_per_second_squared{actor_id="42",axis="x",episode_id="987654321"} 0.10000000149011612`,
		`carla_vehicle_angular_velocity_radians_per_second{actor_id="42",axis="x",episode_id="987654321"} 3.141592653589793`,
		`carla_vehicle_control_throttle_ratio{actor_id="42",episode_id="987654321"} 0.75`,
		`carla_vehicle_control_brake_ratio{actor_id="42",episode_id="987654321"} 0.125`,
		`carla_vehicle_control_steer{actor_id="42",episode_id="987654321"} -0.25`,
		`carla_vehicle_control_hand_brake{actor_id="42",episode_id="987654321"} 1`,
		`carla_vehicle_control_reverse{actor_id="42",episode_id="987654321"} 1`,
		`carla_vehicle_yaw_radians{actor_id="42",episode_id="987654321"} 1.5707963267948966`,
		`carla_vehicle_speed_meters_per_second{actor_id="42",episode_id="987654321"} 5`,
		`carla_vehicle_control_gear{actor_id="42",episode_id="987654321"} -1`,
		`carla_vehicle_info{actor_id="42",blueprint_id="vehicle.tesla.model3",episode_id="987654321",role_name="hero"} 1`,
	} {
		if !strings.Contains(metrics, expected) {
			t.Errorf("missing %s in metrics", expected)
		}
	}
	// No tick requests are sent even while the mock simulation is paused.
	if strings.Contains(metrics, "\ncarla_world_frames_per_second ") {
		t.Fatal("performance published before warm-up")
	}
	frame := loadFixture(t, "world_"+fixtureSuffix(version)+".bin")
	binary.LittleEndian.PutUint64(frame[8:], 130)
	binary.LittleEndian.PutUint64(frame[16:], math.Float64bits(6.5))
	wire.send(frame)
	metrics = awaitMetrics(t, server, func(s string) bool { return strings.Contains(s, "carla_world_frame_number 130\n") })
	if !strings.Contains(metrics, "carla_world_frames_total 10\n") || !strings.Contains(metrics, "carla_world_simulation_seconds_total 0.5\n") {
		t.Fatal("did not accumulate frame progression")
	}
	awaitMetrics(t, server, func(s string) bool {
		return strings.Contains(s, "\ncarla_world_frames_per_second ") && strings.Contains(s, "\ncarla_world_real_time_factor ")
	})
	// Removing the vehicle leaves only the sensor record.
	removed := append(append([]byte(nil), frame[:84]...), frame[84+119:]...)
	binary.LittleEndian.PutUint64(removed[8:], 131)
	wire.send(removed)
	metrics = awaitMetrics(t, server, func(s string) bool { return strings.Contains(s, "carla_world_frame_number 131\n") })
	if strings.Contains(metrics, "\ncarla_vehicle_present{") || !strings.Contains(metrics, "carla_world_vehicles 0\n") {
		t.Fatal("removed vehicle series survived")
	}
	// The episode changes after settings are read but before the final check.
	wire.mu.Lock()
	wire.changeDuringRead = true
	wire.mu.Unlock()
	metrics = scrape(t, server)
	if !strings.Contains(metrics, "carla_rpc_up 0\n") || strings.Contains(metrics, "\ncarla_world_frame_number ") {
		t.Fatal("published mixed-episode values")
	}
	// Recovery uses the new episode, retaining counters without counting the gap.
	binary.LittleEndian.PutUint64(removed[48:], 987654322)
	binary.LittleEndian.PutUint64(removed[8:], 1)
	binary.LittleEndian.PutUint64(removed[16:], math.Float64bits(0.05))
	wire.send(removed)
	awaitMetrics(t, server, func(s string) bool {
		return strings.Contains(s, "carla_world_frame_number 1\n") && strings.Contains(s, "carla_rpc_up 1\n")
	})
	wire.mu.Lock()
	subscriptions := wire.subscriptions
	wire.mu.Unlock()
	wire.disconnectStreams()
	awaitMetrics(t, server, func(s string) bool {
		wire.mu.Lock()
		reconnected := wire.subscriptions > subscriptions
		wire.mu.Unlock()
		return reconnected && strings.Contains(s, "carla_world_frame_number 1\n") && strings.Contains(s, "carla_world_stream_connected 1\n")
	})
	// Unsupported versions suppress world and vehicle state but retain diagnostics.
	wire.mu.Lock()
	wire.version = "0.9.17"
	wire.mu.Unlock()
	metrics = scrape(t, server)
	if !strings.Contains(metrics, "carla_rpc_up 0\n") || strings.Contains(metrics, "\ncarla_world_info{") {
		t.Fatal("unsupported version was accepted")
	}
}

func TestExporterRenegotiatesAfterVersionChange(t *testing.T) {
	wire := newWireServer(t, "0.9.15")
	server := startExporter(t, wire.rpc.Addr().String(), "", time.Second)
	awaitMetrics(t, server, func(s string) bool { return strings.Contains(s, "carla_world_frame_number 120\n") })
	frame := loadFixture(t, "world_0915.bin")
	binary.LittleEndian.PutUint64(frame[8:], 125)
	binary.LittleEndian.PutUint64(frame[16:], math.Float64bits(6.25))
	wire.send(frame)
	awaitMetrics(t, server, func(s string) bool { return strings.Contains(s, "carla_world_frames_total 5\n") })
	for index, version := range []string{"0.9.16", "0.9.12", "0.10.0"} {
		t.Run(version, func(t *testing.T) {
			episode := uint64(987654322 + index)
			wire.restart(version, episode)
			info := fmt.Sprintf(`carla_world_info{map="Carla/Maps/Town01",version="%s"} 1`, version)
			vehicle := fmt.Sprintf(`carla_vehicle_speed_meters_per_second{actor_id="42",episode_id="%d"} 5`, episode)
			metrics := awaitMetrics(t, server, func(s string) bool {
				return strings.Contains(s, info) && strings.Contains(s, "carla_world_frame_number 1\n") && strings.Contains(s, vehicle)
			})
			if !strings.Contains(metrics, "carla_rpc_up 1\n") || !strings.Contains(metrics, "carla_world_stream_connected 1\n") || !strings.Contains(metrics, "carla_world_fixed_delta_seconds 0.05\n") {
				t.Fatal("version change did not restore stream and settings")
			}
			if !strings.Contains(metrics, "carla_world_frames_total 5\n") || !strings.Contains(metrics, "carla_world_simulation_seconds_total 0.25\n") {
				t.Fatal("version change counted an unobserved gap or reset counters")
			}
			if strings.Contains(metrics, "\ncarla_world_frames_per_second ") {
				t.Fatal("version change did not reset the performance window")
			}
		})
	}
}

func TestLiveCARLA(t *testing.T) {
	address := os.Getenv("CARLA_ADDRESS")
	if address == "" {
		t.Skip("set CARLA_ADDRESS=host:port to run the read-only CARLA integration test")
	}
	server := startExporter(t, address, os.Getenv("CARLA_STREAM_ADDRESS"), 5*time.Second)
	metrics := scrape(t, server)
	if !strings.Contains(metrics, "carla_rpc_up 1\n") {
		t.Fatalf("live CARLA RPC collection failed:\n%s", metrics)
	}
	metrics = awaitMetrics(t, server, func(s string) bool { return strings.Contains(s, "carla_world_stream_connected 1\n") })
	if expected := os.Getenv("CARLA_EXPECT_VERSION"); expected != "" && !strings.Contains(metrics, `version="`+expected+`"`) {
		t.Fatalf("expected CARLA version %s in live metrics:\n%s", expected, metrics)
	}
	t.Log("CARLA RPC and stream connection validated; no tick or mutation requests were sent")
	// A paused synchronous world need not send a first snapshot. Snapshot checks
	// are mandatory only when the caller explicitly expects a running simulation.
	if os.Getenv("CARLA_EXPECT_FRAMES") == "1" {
		metrics = awaitMetrics(t, server, func(s string) bool { return strings.Contains(s, "\ncarla_world_frame_number ") })
		t.Log("Live world snapshot decoded and exposed successfully")
	}
}
