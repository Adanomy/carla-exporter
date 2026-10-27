package carla

import (
	"context"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/Adanomy/carla-exporter/internal/carla/protocol"
)

// Client exposes only read-only CARLA operations.
type Client struct{ rpc *protocol.RPC }

func NewClient(address string, maxMessage int) *Client {
	return &Client{rpc: protocol.NewRPC(address, maxMessage)}
}

func (c *Client) Close() error { return c.rpc.Close() }

// Protocol reads the server version and selects its immutable wire profile.
func (c *Client) Protocol(ctx context.Context) (protocol.Profile, error) {
	data, err := c.rpc.Call(ctx, "version")
	if err != nil {
		return protocol.Profile{}, err
	}
	var version string
	if err := msgpack.Unmarshal(data, &version); err != nil {
		return protocol.Profile{}, err
	}
	return protocol.ForVersion(version)
}

func (c *Client) Episode(ctx context.Context) (protocol.EpisodeInfo, error) {
	data, err := c.rpc.Call(ctx, "get_episode_info")
	if err != nil {
		return protocol.EpisodeInfo{}, err
	}
	return protocol.DecodeEpisode(data)
}

func (c *Client) Settings(ctx context.Context, profile protocol.Profile) (protocol.Settings, error) {
	data, err := c.rpc.Call(ctx, "get_episode_settings")
	if err != nil {
		return protocol.Settings{}, err
	}
	return profile.DecodeSettings(data)
}

func (c *Client) Map(ctx context.Context) (string, error) {
	data, err := c.rpc.Call(ctx, "get_map_info")
	if err != nil {
		return "", err
	}
	return protocol.DecodeMap(data)
}

func (c *Client) Actors(ctx context.Context, ids []uint32) ([]protocol.ActorDescription, error) {
	data, err := c.rpc.Call(ctx, "get_actors_by_id", ids)
	if err != nil {
		return nil, err
	}
	return protocol.DecodeActors(data)
}
