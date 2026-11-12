package mqtt

import (
	"bufio"
	"encoding/binary"
	"log"
	"net"
	"sync"
	"sync/atomic"
)

// Broker is an in-process MQTT 3.1.1 broker for QoS 0 traffic.
type Broker struct {
	mu       sync.RWMutex
	subs     map[*conn][]string
	retained map[string][]byte
	ln       net.Listener
	Routed   atomic.Int64 // messages delivered to subscribers
	Logger   *log.Logger
}

type conn struct {
	c  net.Conn
	wm sync.Mutex
	id string
}

func (c *conn) send(p *Packet) error {
	c.wm.Lock()
	defer c.wm.Unlock()
	_, err := c.c.Write(p.Bytes())
	return err
}

func NewBroker() *Broker {
	return &Broker{subs: map[*conn][]string{}, retained: map[string][]byte{}}
}

// Listen starts accepting clients on addr (for example "127.0.0.1:1883") and returns the bound address.
func (b *Broker) Listen(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	b.ln = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go b.serve(&conn{c: c})
		}
	}()
	return ln.Addr().String(), nil
}

func (b *Broker) Close() error {
	if b.ln != nil {
		return b.ln.Close()
	}
	return nil
}

func (b *Broker) serve(c *conn) {
	defer func() {
		b.mu.Lock()
		delete(b.subs, c)
		b.mu.Unlock()
		c.c.Close()
	}()
	r := bufio.NewReader(c.c)
	first, err := ReadPacket(r)
	if err != nil || first.Type != CONNECT {
		return
	}
	// CONNECT: protocol name, level, flags, keep-alive, then client id
	if _, rest, err := readStr(first.Body); err == nil && len(rest) >= 4 {
		if id, _, err := readStr(rest[4:]); err == nil {
			c.id = id
		}
	}
	c.send(&Packet{Type: CONNACK, Body: []byte{0, 0}})
	for {
		p, err := ReadPacket(r)
		if err != nil {
			return
		}
		switch p.Type {
		case PUBLISH:
			topic, payload, retain, err := ParsePublish(p)
			if err != nil {
				return
			}
			b.publish(topic, payload, retain)
		case SUBSCRIBE:
			if len(p.Body) < 2 {
				return
			}
			pid := p.Body[:2]
			rest := p.Body[2:]
			var filters []string
			var granted []byte
			for len(rest) > 0 {
				f, r2, err := readStr(rest)
				if err != nil || len(r2) < 1 {
					return
				}
				filters = append(filters, f)
				granted = append(granted, 0)
				rest = r2[1:]
			}
			b.mu.Lock()
			b.subs[c] = append(b.subs[c], filters...)
			var ret []*Packet
			for t, v := range b.retained {
				for _, f := range filters {
					if Match(f, t) {
						ret = append(ret, Publish(t, v, true))
						break
					}
				}
			}
			b.mu.Unlock()
			c.send(&Packet{Type: SUBACK, Body: append(append([]byte{}, pid...), granted...)})
			for _, rp := range ret {
				c.send(rp)
			}
		case UNSUBSCRIBE:
			if len(p.Body) < 2 {
				return
			}
			pid := binary.BigEndian.Uint16(p.Body[:2])
			rest := p.Body[2:]
			b.mu.Lock()
			for len(rest) > 0 {
				f, r2, err := readStr(rest)
				if err != nil {
					break
				}
				kept := b.subs[c][:0]
				for _, s := range b.subs[c] {
					if s != f {
						kept = append(kept, s)
					}
				}
				b.subs[c] = kept
				rest = r2
			}
			b.mu.Unlock()
			body := make([]byte, 2)
			binary.BigEndian.PutUint16(body, pid)
			c.send(&Packet{Type: UNSUBACK, Body: body})
		case PINGREQ:
			c.send(&Packet{Type: PINGRESP})
		case DISCONNECT:
			return
		}
	}
}

func (b *Broker) publish(topic string, payload []byte, retain bool) {
	b.mu.Lock()
	if retain {
		if len(payload) == 0 {
			delete(b.retained, topic)
		} else {
			b.retained[topic] = append([]byte{}, payload...)
		}
	}
	var targets []*conn
	for c, fs := range b.subs {
		for _, f := range fs {
			if Match(f, topic) {
				targets = append(targets, c)
				break
			}
		}
	}
	b.mu.Unlock()
	p := Publish(topic, payload, false)
	for _, c := range targets {
		if c.send(p) == nil {
			b.Routed.Add(1)
		}
	}
}
