// Package protocol implements the read-only wire formats used by supported CARLA releases.
package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ReadMessage reads one MessagePack object without reading into the next one.
// Lengths, nesting, and the total byte budget are checked before allocation.
func ReadMessage(r io.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("invalid MessagePack size limit: %d", limit)
	}
	p := messageReader{r: r, limit: limit}
	if err := p.readValue(0); err != nil {
		return nil, err
	}
	return p.data, nil
}

type messageReader struct {
	r     io.Reader
	limit int
	data  []byte
}

func (p *messageReader) take(n uint64) ([]byte, error) {
	if n > uint64(p.limit-len(p.data)) {
		return nil, fmt.Errorf("MessagePack message exceeds %d bytes", p.limit)
	}
	start := len(p.data)
	p.data = append(p.data, make([]byte, int(n))...)
	b := p.data[start:]
	_, err := io.ReadFull(p.r, b)
	return b, err
}

func (p *messageReader) children(n uint64, depth int) error {
	if n > 65536 {
		return fmt.Errorf("MessagePack container exceeds 65536 entries")
	}
	if n > uint64(p.limit-len(p.data)) {
		return fmt.Errorf("MessagePack container exceeds byte budget")
	}
	for range n {
		if err := p.readValue(depth + 1); err != nil {
			return err
		}
	}
	return nil
}

func (p *messageReader) readValue(depth int) error {
	if depth > 32 {
		return fmt.Errorf("MessagePack nesting exceeds 32 levels")
	}
	b, err := p.take(1)
	if err != nil {
		return err
	}
	c := b[0]
	switch {
	case c <= 0x7f || c >= 0xe0 || c == 0xc0 || c == 0xc2 || c == 0xc3:
		return nil
	case c >= 0xa0 && c <= 0xbf:
		_, err = p.take(uint64(c & 31))
		return err
	case c >= 0x90 && c <= 0x9f:
		return p.children(uint64(c&15), depth)
	case c >= 0x80 && c <= 0x8f:
		return p.children(2*uint64(c&15), depth)
	}
	var width int
	switch c {
	case 0xcc, 0xd0:
		width = 1
	case 0xcd, 0xd1:
		width = 2
	case 0xce, 0xd2, 0xca:
		width = 4
	case 0xcf, 0xd3, 0xcb:
		width = 8
	case 0xd4:
		width = 2
	case 0xd5:
		width = 3
	case 0xd6:
		width = 5
	case 0xd7:
		width = 9
	case 0xd8:
		width = 17
	case 0xc4, 0xc7, 0xd9:
		width = 1
	case 0xc5, 0xc8, 0xda, 0xdc, 0xde:
		width = 2
	case 0xc6, 0xc9, 0xdb, 0xdd, 0xdf:
		width = 4
	default:
		return fmt.Errorf("unsupported MessagePack tag 0x%02x", c)
	}
	b, err = p.take(uint64(width))
	if err != nil {
		return err
	}
	switch c {
	case 0xc4, 0xc7, 0xd9, 0xc5, 0xc8, 0xda, 0xdc, 0xde, 0xc6, 0xc9, 0xdb, 0xdd, 0xdf:
		var n uint64
		switch width {
		case 1:
			n = uint64(b[0])
		case 2:
			n = uint64(binary.BigEndian.Uint16(b))
		case 4:
			n = uint64(binary.BigEndian.Uint32(b))
		}
		switch c {
		case 0xdc, 0xdd:
			return p.children(n, depth)
		case 0xde, 0xdf:
			return p.children(2*n, depth)
		case 0xc7, 0xc8, 0xc9:
			n++ // Extension type byte.
		}
		_, err = p.take(n)
	}
	return err
}
