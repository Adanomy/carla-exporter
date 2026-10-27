package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../../../testdata/protocol", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSharedRPCFixtures(t *testing.T) {
	for _, name := range []string{"episode_array_0915.msgpack", "episode_bin_0915.msgpack"} {
		t.Run(name, func(t *testing.T) {
			episode, err := DecodeEpisode(fixture(t, name))
			if err != nil {
				t.Fatal(err)
			}
			address, err := episode.Token.Address("localhost")
			if err != nil || episode.ID != 987654321 || address != "localhost:2001" || binary.LittleEndian.Uint32(episode.Token[:4]) != 7 {
				t.Fatalf("unexpected episode: %+v, address %q, error %v", episode, address, err)
			}
		})
	}
	name, err := DecodeMap(fixture(t, "map_0915.msgpack"))
	if err != nil || name != "Carla/Maps/Town01" {
		t.Fatalf("map: %q, %v", name, err)
	}
	actors, err := DecodeActors(fixture(t, "actors_0915.msgpack"))
	if err != nil || len(actors) != 2 {
		t.Fatalf("actors: %+v, %v", actors, err)
	}
	if actors[0].ID != 42 || actors[0].BlueprintID != "vehicle.tesla.model3" || actors[0].RoleName != "hero" {
		t.Fatalf("vehicle description: %+v", actors[0])
	}
}

func TestWorldRejectsInvalidLayouts(t *testing.T) {
	for _, release := range releaseFixtures {
		t.Run(release.version, func(t *testing.T) {
			profile := mustProfile(t, release.version)
			valid := fixture(t, "world_"+release.suffix+".bin")
			for _, n := range []int{0, 47, 83, 85, len(valid) - 1} {
				if _, err := profile.DecodeWorld(valid[:n]); err == nil {
					t.Fatalf("accepted truncated length %d", n)
				}
			}
			for _, mutate := range []func([]byte){
				func(b []byte) { binary.LittleEndian.PutUint64(b, 999) },
				func(b []byte) { binary.LittleEndian.PutUint64(b[16:], math.Float64bits(math.NaN())) },
				func(b []byte) { binary.LittleEndian.PutUint32(b[64:], math.Float32bits(-0.05)) },
				func(b []byte) { binary.LittleEndian.PutUint32(b[84+5:], math.Float32bits(float32(math.Inf(1)))) },
				func(b []byte) { binary.LittleEndian.PutUint32(b[84+119:], 42) },
			} {
				b := bytes.Clone(valid)
				mutate(b)
				if _, err := profile.DecodeWorld(b); err == nil {
					t.Fatal("accepted invalid snapshot")
				}
			}
		})
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], math.MaxUint32)
	if _, err := ReadFrame(bytes.NewReader(header[:]), 1024); err == nil {
		t.Fatal("accepted oversized frame")
	}
}

func TestBoundedMessageReader(t *testing.T) {
	first := fixture(t, "version_response_0915.msgpack")
	input := bytes.NewReader(append(bytes.Clone(first), 0xc0))
	got, err := ReadMessage(input, len(first))
	if err != nil || !bytes.Equal(got, first) || input.Len() != 1 {
		t.Fatalf("message boundary: len=%d, unread=%d, error=%v", len(got), input.Len(), err)
	}
	for _, b := range [][]byte{
		{0xdd, 0xff, 0xff, 0xff, 0xff}, {0xdb, 0xff, 0xff, 0xff, 0xff},
		{0xdf, 0xff, 0xff, 0xff, 0xff}, {0xc1}, bytes.Repeat([]byte{0x91}, 40), first[:len(first)-1],
	} {
		if _, err := ReadMessage(bytes.NewReader(b), 1024); err == nil {
			t.Fatalf("accepted invalid MessagePack: %x", b)
		}
	}
}

func TestRPCWireAndResponseValidation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverResult := make(chan error, 1)
	go func() {
		for attempt := range 2 {
			conn, err := listener.Accept()
			if err != nil {
				serverResult <- err
				return
			}
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			data, err := ReadMessage(conn, 4096)
			if err != nil {
				_ = conn.Close()
				serverResult <- err
				return
			}
			fields, err := array(data, 4)
			if err != nil {
				_ = conn.Close()
				serverResult <- err
				return
			}
			var id uint32
			var method string
			_ = msgpack.Unmarshal(fields[1], &id)
			_ = msgpack.Unmarshal(fields[2], &method)
			params, err := array(fields[3], 1)
			if err != nil || method != "version" || !bytes.Equal(params[0], []byte{0x91, 0xc2}) {
				_ = conn.Close()
				serverResult <- errors.New("invalid CARLA RPC method or metadata")
				return
			}
			if attempt == 0 {
				id++
			}
			response, err := msgpack.Marshal([]any{1, id, nil, []any{[]any{1, "0.9.15"}}})
			if err == nil {
				_, err = conn.Write(response)
			}
			_ = conn.Close()
			if err != nil {
				serverResult <- err
				return
			}
		}
		serverResult <- nil
	}()
	rpc := NewRPC(listener.Addr().String(), 4096)
	t.Cleanup(func() { _ = rpc.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := rpc.Call(ctx, "version"); err == nil {
		t.Fatal("accepted mismatched response ID")
	}
	data, err := rpc.Call(ctx, "version")
	if err != nil {
		t.Fatal(err)
	}
	var version string
	if err := msgpack.Unmarshal(data, &version); err != nil || version != "0.9.15" {
		t.Fatalf("version %q: %v", version, err)
	}
	if _, err := rpc.Call(ctx, "tick_cue"); err == nil {
		t.Fatal("permitted a mutating method")
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestRPCCancellationInterruptsRead(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = ReadMessage(conn, 4096)
		close(received)
		_, _ = io.Copy(io.Discard, conn)
	}()
	rpc := NewRPC(listener.Addr().String(), 4096)
	defer rpc.Close()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := rpc.Call(ctx, "version"); result <- err }()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("server did not receive request")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RPC read did not honor cancellation")
	}
	<-closed
}

func TestCARLAErrorEnvelope(t *testing.T) {
	data, _ := msgpack.Marshal([]any{[]any{0, []any{"episode unavailable"}}})
	if _, err := unwrap(data); err == nil {
		t.Fatal("accepted CARLA error response")
	}
	data, _ = msgpack.Marshal([]any{[]any{2, "unknown"}})
	if _, err := unwrap(data); err == nil {
		t.Fatal("accepted unknown response variant")
	}
}

func FuzzDecodeWorld(f *testing.F) {
	for _, release := range releaseFixtures {
		f.Add(release.version, fixture(f, "world_"+release.suffix+".bin"))
	}
	f.Add("0.9.15", []byte{})
	f.Fuzz(func(t *testing.T, version string, data []byte) {
		profile, err := ForVersion(version)
		if err == nil && len(data) <= 1<<20 {
			_, _ = profile.DecodeWorld(data)
		}
	})
}

func FuzzDecodeSettings(f *testing.F) {
	for _, release := range releaseFixtures {
		f.Add(release.version, fixture(f, "settings_"+release.suffix+".msgpack"))
		f.Add(release.version, fixture(f, "settings_variable_"+release.suffix+".msgpack"))
	}
	f.Fuzz(func(t *testing.T, version string, data []byte) {
		profile, err := ForVersion(version)
		if err == nil && len(data) <= 4096 {
			_, _ = profile.DecodeSettings(data)
		}
	})
}

func FuzzReadMessage(f *testing.F) {
	f.Add(fixture(f, "version_response_0915.msgpack"))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = ReadMessage(bytes.NewReader(data), 4096) })
}
