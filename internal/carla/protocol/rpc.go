package protocol

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// RPC serializes calls on a reusable connection. A failed exchange discards the
// connection so a late response can never be mistaken for the next response.
type RPC struct {
	address    string
	maxMessage int
	gate       chan struct{}
	conn       net.Conn
	reader     *bufio.Reader
	nextID     uint32
}

func NewRPC(address string, maxMessage int) *RPC {
	return &RPC{address: address, maxMessage: maxMessage, gate: make(chan struct{}, 1)}
}

// Call permits only the five read-only methods needed by the exporter.
func (r *RPC) Call(ctx context.Context, method string, args ...any) (result msgpack.RawMessage, err error) {
	switch method {
	case "version", "get_episode_info", "get_map_info", "get_episode_settings", "get_actors_by_id":
	default:
		return nil, fmt.Errorf("RPC method %q is not permitted", method)
	}
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.conn == nil {
		r.conn, err = (&net.Dialer{KeepAlive: 15 * time.Second}).DialContext(ctx, "tcp", r.address)
		if err != nil {
			return nil, fmt.Errorf("connect RPC: %w", err)
		}
		r.reader = bufio.NewReader(r.conn)
	}
	conn := r.conn
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Second)
	}
	if err = conn.SetDeadline(deadline); err != nil {
		r.disconnect()
		return nil, err
	}
	canceled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(canceled) })
	defer func() {
		if !stop() {
			<-canceled
		}
		if err != nil || ctx.Err() != nil {
			r.disconnect()
			if ctx.Err() != nil {
				err = ctx.Err()
			}
		}
	}()
	r.nextID++
	params := append([]any{[]any{false}}, args...) // CARLA Metadata::MakeSync().
	request, err := msgpack.Marshal([]any{uint8(0), r.nextID, method, params})
	if err != nil {
		return nil, err
	}
	if len(request) > r.maxMessage {
		return nil, fmt.Errorf("RPC request exceeds size limit")
	}
	for len(request) > 0 {
		n, writeErr := conn.Write(request)
		if writeErr != nil {
			return nil, writeErr
		}
		if n == 0 {
			return nil, errors.New("RPC write made no progress")
		}
		request = request[n:]
	}
	data, err := ReadMessage(r.reader, r.maxMessage)
	if err != nil {
		return nil, fmt.Errorf("read RPC response: %w", err)
	}
	response, err := array(data, 4)
	if err != nil {
		return nil, fmt.Errorf("decode RPC response: %w", err)
	}
	var kind, id uint32
	if err = msgpack.Unmarshal(response[0], &kind); err != nil {
		return nil, err
	}
	if err = msgpack.Unmarshal(response[1], &id); err != nil {
		return nil, err
	}
	if kind != 1 || id != r.nextID {
		return nil, fmt.Errorf("unexpected RPC response type or ID")
	}
	if !isNil(response[2]) {
		return nil, fmt.Errorf("RPC %s failed: %x", method, response[2])
	}
	return unwrap(response[3])
}

func (r *RPC) disconnect() {
	if r.conn != nil {
		_ = r.conn.Close()
	}
	r.conn = nil
	r.reader = nil
}

func (r *RPC) Close() error {
	r.gate <- struct{}{}
	defer func() { <-r.gate }()
	r.disconnect()
	return nil
}

func array(data []byte, length int) ([]msgpack.RawMessage, error) {
	var fields []msgpack.RawMessage
	if err := msgpack.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if length >= 0 && len(fields) != length {
		return nil, fmt.Errorf("expected %d fields, got %d", length, len(fields))
	}
	return fields, nil
}

func isNil(data []byte) bool { return len(data) == 0 || (len(data) == 1 && data[0] == 0xc0) }

// CARLA Response<T> is [[variant_index, value]], not the bare RPC result.
func unwrap(data []byte) (msgpack.RawMessage, error) {
	outer, err := array(data, 1)
	if err != nil {
		return nil, err
	}
	variant, err := array(outer[0], 2)
	if err != nil {
		return nil, err
	}
	var index uint64
	if err = msgpack.Unmarshal(variant[0], &index); err != nil {
		return nil, err
	}
	switch index {
	case 1:
		return variant[1], nil
	case 0:
		fields, err := array(variant[1], 1)
		if err != nil {
			return nil, err
		}
		var message string
		if err := msgpack.Unmarshal(fields[0], &message); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("CARLA returned an error: %s", message)
	default:
		return nil, fmt.Errorf("unsupported CARLA response variant %d", index)
	}
}
