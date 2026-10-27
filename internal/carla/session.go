package carla

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"github.com/Adanomy/carla-exporter/internal/carla/protocol"
)

type SessionOptions struct {
	Address           string
	StreamAddress     string
	Timeout           time.Duration
	PerformanceWindow time.Duration
	ReconnectMin      time.Duration
	ReconnectMax      time.Duration
	MaxMessageBytes   int
}

type sample struct {
	at      time.Time
	frames  float64
	seconds float64
}

// Session receives frames continuously; metadata is read when Prometheus scrapes.
// All published snapshots are immutable after being installed under mu.
type Session struct {
	client             *Client
	options            SessionOptions
	logger             *slog.Logger
	readGate           chan struct{}
	mu                 sync.Mutex
	conn               net.Conn
	token              protocol.Token
	profile            protocol.Profile
	raw                *protocol.WorldState
	lastSnapshotAt     time.Time
	lastEpisodeID      uint64
	lastFrameNumber    uint64
	framesTotal        float64
	secondsTotal       float64
	history            []sample
	descriptionEpisode uint64
	descriptionVersion string
	descriptions       map[uint32]protocol.ActorDescription
}

func NewSession(client *Client, options SessionOptions, logger *slog.Logger) *Session {
	return &Session{client: client, options: options, logger: logger, readGate: make(chan struct{}, 1)}
}

func (s *Session) Run(ctx context.Context) {
	delay := s.options.ReconnectMin
	for ctx.Err() == nil {
		started := time.Now()
		err := s.receive(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= s.options.ReconnectMax {
			delay = s.options.ReconnectMin
		}
		s.logger.Warn("CARLA stream disconnected", "error", err, "retry_delay", delay)
		jitter := time.Duration(rand.Int64N(max(int64(delay/4), 1)))
		timer := time.NewTimer(delay + jitter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, s.options.ReconnectMax)
	}
}

func (s *Session) receive(ctx context.Context) error {
	setup, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()
	profile, err := s.client.Protocol(setup)
	if err != nil {
		return err
	}
	episode, err := s.client.Episode(setup)
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(s.options.Address)
	if err != nil {
		return err
	}
	address, err := episode.Token.Address(host)
	if err != nil {
		return err
	}
	if s.options.StreamAddress != "" {
		address = s.options.StreamAddress
	}
	conn, err := (&net.Dialer{KeepAlive: 15 * time.Second}).DialContext(setup, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect world stream: %w", err)
	}
	defer conn.Close()
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	deadline, _ := setup.Deadline()
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	// Only the four-byte stream ID is sent, not the entire endpoint token.
	id := episode.Token[:4]
	for len(id) > 0 {
		n, err := conn.Write(id)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("stream subscription write made no progress")
		}
		id = id[n:]
	}
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		return err
	}
	cancel()
	s.mu.Lock()
	s.conn, s.token = conn, episode.Token
	s.profile = profile
	s.raw, s.history = nil, nil
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.conn, s.raw, s.history = nil, nil, nil
		s.mu.Unlock()
	}()
	s.logger.Info("CARLA world stream connected", "address", address, "stream_id", binary.LittleEndian.Uint32(episode.Token[:4]), "version", profile.Version())
	for {
		// No read deadline: a synchronous simulation may legitimately have no ticks.
		data, err := protocol.ReadFrame(conn, s.options.MaxMessageBytes)
		if err != nil {
			return err
		}
		world, err := profile.DecodeWorld(data)
		if err != nil {
			return fmt.Errorf("decode world snapshot: %w", err)
		}
		s.mu.Lock()
		s.observe(&world, time.Now())
		s.mu.Unlock()
	}
}

func (s *Session) observe(world *protocol.WorldState, now time.Time) {
	previous := s.raw
	if previous != nil && previous.EpisodeID == world.EpisodeID {
		if world.Frame <= previous.Frame {
			return
		}
		if world.ElapsedSeconds >= previous.ElapsedSeconds {
			s.framesTotal += float64(world.Frame - previous.Frame)
			s.secondsTotal += world.ElapsedSeconds - previous.ElapsedSeconds
		} else {
			s.history = nil
		}
	} else {
		s.history = nil
	}
	s.raw = world
	if s.lastSnapshotAt.IsZero() || s.lastEpisodeID != world.EpisodeID || s.lastFrameNumber != world.Frame {
		s.lastSnapshotAt = now
		s.lastEpisodeID, s.lastFrameNumber = world.EpisodeID, world.Frame
	}
	point := sample{now, s.framesTotal, s.secondsTotal}
	spacing := s.options.PerformanceWindow / 1000
	if len(s.history) == 0 || now.Sub(s.history[len(s.history)-1].at) >= spacing {
		s.history = append(s.history, point)
	}
	s.prune(now)
}

func (s *Session) prune(now time.Time) {
	cutoff := now.Add(-s.options.PerformanceWindow)
	index := 0
	for index+1 < len(s.history) && !s.history[index+1].at.After(cutoff) {
		index++
	}
	if index > 0 {
		s.history = append(s.history[:0], s.history[index:]...)
	}
}

func (s *Session) view(now time.Time) (Observation, *protocol.WorldState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	o := Observation{StreamConnected: s.conn != nil, LastSnapshotAt: s.lastSnapshotAt, FramesTotal: s.framesTotal, SimulationSecondsTotal: s.secondsTotal}
	if !o.LastSnapshotAt.IsZero() {
		o.FrameAgeSeconds = max(0, now.Sub(o.LastSnapshotAt).Seconds())
	}
	if s.conn != nil && len(s.history) > 0 && now.Sub(s.history[0].at) >= s.options.PerformanceWindow {
		base := s.history[0]
		// Counters are step functions: interpolating between frame arrivals
		// would spread a resumed frame across an earlier idle interval.
		o.Performance = &Performance{max(0, s.framesTotal-base.frames) / s.options.PerformanceWindow.Seconds(), max(0, s.secondsTotal-base.seconds) / s.options.PerformanceWindow.Seconds()}
	}
	return o, s.raw
}

// Read returns a single scrape's data, checking the episode again after reading
// metadata so a map change cannot join one world's actors to another's settings.
func (s *Session) Read(ctx context.Context) (Observation, error) {
	select {
	case s.readGate <- struct{}{}:
		defer func() { <-s.readGate }()
	case <-ctx.Done():
		o, _ := s.view(time.Now())
		o.Performance = nil
		return o, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()
	result, err := s.read(ctx)
	if err != nil {
		o, _ := s.view(time.Now())
		o.Performance = nil
		return o, err
	}
	return result, nil
}

func (s *Session) read(ctx context.Context) (Observation, error) {
	profile, err := s.client.Protocol(ctx)
	if err != nil {
		return Observation{}, err
	}
	episode, err := s.client.Episode(ctx)
	if err != nil {
		return Observation{}, err
	}
	s.mu.Lock()
	if s.conn != nil && (s.token != episode.Token || s.profile != profile) {
		_ = s.conn.Close()
		s.conn = nil
		s.raw, s.history = nil, nil
	}
	s.mu.Unlock()
	settings, err := s.client.Settings(ctx, profile)
	if err != nil {
		return Observation{}, err
	}
	mapName, err := s.client.Map(ctx)
	if err != nil {
		return Observation{}, err
	}
	o, raw := s.view(time.Now())
	if s.descriptions == nil || s.descriptionEpisode != episode.ID || s.descriptionVersion != profile.Version() {
		s.descriptions = make(map[uint32]protocol.ActorDescription)
		s.descriptionEpisode = episode.ID
		s.descriptionVersion = profile.Version()
	}
	if raw != nil && raw.EpisodeID == episode.ID && o.StreamConnected {
		current := make(map[uint32]struct{}, len(raw.Actors))
		var missing []uint32
		for _, actor := range raw.Actors {
			current[actor.ID] = struct{}{}
			if _, ok := s.descriptions[actor.ID]; !ok {
				missing = append(missing, actor.ID)
			}
		}
		for id := range s.descriptions {
			if _, ok := current[id]; !ok {
				delete(s.descriptions, id)
			}
		}
		// Bound each RPC request even for large actor registries.
		for start := 0; start < len(missing); start += 512 {
			actors, err := s.client.Actors(ctx, missing[start:min(start+512, len(missing))])
			if err != nil {
				return Observation{}, err
			}
			for _, actor := range actors {
				s.descriptions[actor.ID] = actor
			}
		}
		o.Snapshot, err = convertSnapshot(raw, s.descriptions)
		if err != nil {
			return Observation{}, err
		}
	} else {
		o.Performance = nil
	}
	after, err := s.client.Episode(ctx)
	if err != nil {
		return Observation{}, err
	}
	if episode != after {
		return Observation{}, fmt.Errorf("CARLA episode changed during collection")
	}
	s.mu.Lock()
	o.StreamConnected = s.conn != nil
	valid := s.conn != nil && s.token == episode.Token && s.profile == profile && s.raw != nil && s.raw.EpisodeID == episode.ID
	s.mu.Unlock()
	if !valid {
		o.Snapshot, o.Performance = nil, nil
	}
	o.Version, o.Map = profile.Version(), mapName
	o.Settings = &Settings{Synchronous: settings.Synchronous, NoRendering: settings.NoRendering, FixedDeltaSeconds: settings.FixedDeltaSeconds, Substepping: settings.Substepping, MaxSubstepDeltaSeconds: settings.MaxSubstepDeltaSeconds, MaxSubsteps: settings.MaxSubsteps}
	return o, nil
}
