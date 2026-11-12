// Package mqtt is a small MQTT 3.1.1 broker and client (QoS 0, retained messages, + and # wildcards),
// written with the Go standard library only.
package mqtt

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

const (
	CONNECT     = 1
	CONNACK     = 2
	PUBLISH     = 3
	SUBSCRIBE   = 8
	SUBACK      = 9
	UNSUBSCRIBE = 10
	UNSUBACK    = 11
	PINGREQ     = 12
	PINGRESP    = 13
	DISCONNECT  = 14
)

// Packet is one MQTT control packet: the fixed-header type and flags, and the raw variable part.
type Packet struct {
	Type  byte
	Flags byte
	Body  []byte
}

func encodeLength(n int) []byte {
	var out []byte
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

// ReadPacket reads one packet. Remaining length is limited to 1 MiB.
func ReadPacket(r *bufio.Reader) (*Packet, error) {
	h, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	n, mul := 0, 1
	for i := 0; ; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		n += int(b&0x7f) * mul
		if b&0x80 == 0 {
			break
		}
		mul *= 128
		if i == 3 {
			return nil, errors.New("mqtt: malformed remaining length")
		}
	}
	if n > 1<<20 {
		return nil, errors.New("mqtt: packet too large")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return &Packet{Type: h >> 4, Flags: h & 0x0f, Body: body}, nil
}

func (p *Packet) Bytes() []byte {
	out := []byte{p.Type<<4 | p.Flags}
	out = append(out, encodeLength(len(p.Body))...)
	return append(out, p.Body...)
}

func str(s string) []byte {
	b := make([]byte, 2, 2+len(s))
	binary.BigEndian.PutUint16(b, uint16(len(s)))
	return append(b, s...)
}

func readStr(b []byte) (string, []byte, error) {
	if len(b) < 2 {
		return "", nil, errors.New("mqtt: short string")
	}
	n := int(binary.BigEndian.Uint16(b))
	if len(b) < 2+n {
		return "", nil, errors.New("mqtt: short string")
	}
	return string(b[2 : 2+n]), b[2+n:], nil
}

// Publish builds a QoS 0 PUBLISH packet.
func Publish(topic string, payload []byte, retain bool) *Packet {
	f := byte(0)
	if retain {
		f = 1
	}
	return &Packet{Type: PUBLISH, Flags: f, Body: append(str(topic), payload...)}
}

// ParsePublish returns topic, payload and the retain flag of a PUBLISH (QoS 0 or 1).
func ParsePublish(p *Packet) (string, []byte, bool, error) {
	topic, rest, err := readStr(p.Body)
	if err != nil {
		return "", nil, false, err
	}
	if (p.Flags>>1)&3 > 0 { // QoS 1/2 carry a packet id; we deliver at QoS 0
		if len(rest) < 2 {
			return "", nil, false, errors.New("mqtt: missing packet id")
		}
		rest = rest[2:]
	}
	return topic, rest, p.Flags&1 == 1, nil
}

// Match reports whether a topic matches a filter with + and # wildcards.
func Match(filter, topic string) bool {
	f, t := split(filter), split(topic)
	for i, part := range f {
		if part == "#" {
			return true
		}
		if i >= len(t) {
			return false
		}
		if part != "+" && part != t[i] {
			return false
		}
	}
	return len(f) == len(t)
}

func split(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
