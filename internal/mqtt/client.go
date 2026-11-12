package mqtt

import (
	"bufio"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"
)

// Handler receives messages for a subscription.
type Handler func(topic string, payload []byte)

// Client is a minimal QoS 0 MQTT client.
type Client struct {
	c       net.Conn
	wm      sync.Mutex
	mu      sync.RWMutex
	routes  []route
	pid     uint16
	acks    chan struct{}
	closed  chan struct{}
	stopped bool
}

type route struct {
	filter string
	h      Handler
}

// Dial connects with a client id and starts reading in the background.
func Dial(addr, clientID string) (*Client, error) {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	body := append(str("MQTT"), 4, 0x02, 0, 60) // level 4, clean session, keep-alive 60 s
	body = append(body, str(clientID)...)
	if _, err := c.Write((&Packet{Type: CONNECT, Body: body}).Bytes()); err != nil {
		return nil, err
	}
	r := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	p, err := ReadPacket(r)
	if err != nil || p.Type != CONNACK || len(p.Body) < 2 || p.Body[1] != 0 {
		c.Close()
		return nil, errors.New("mqtt: connection refused")
	}
	c.SetReadDeadline(time.Time{})
	cl := &Client{c: c, acks: make(chan struct{}, 16), closed: make(chan struct{})}
	go cl.loop(r)
	go cl.keepalive()
	return cl, nil
}

func (cl *Client) keepalive() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			cl.write(&Packet{Type: PINGREQ})
		case <-cl.closed:
			return
		}
	}
}

func (cl *Client) loop(r *bufio.Reader) {
	defer close(cl.closed)
	for {
		p, err := ReadPacket(r)
		if err != nil {
			return
		}
		switch p.Type {
		case PUBLISH:
			topic, payload, _, err := ParsePublish(p)
			if err != nil {
				continue
			}
			cl.mu.RLock()
			for _, rt := range cl.routes {
				if Match(rt.filter, topic) {
					rt.h(topic, payload)
				}
			}
			cl.mu.RUnlock()
		case SUBACK:
			cl.acks <- struct{}{}
		}
	}
}

func (cl *Client) write(p *Packet) error {
	cl.wm.Lock()
	defer cl.wm.Unlock()
	_, err := cl.c.Write(p.Bytes())
	return err
}

// Subscribe registers a handler and waits for the broker's SUBACK.
func (cl *Client) Subscribe(filter string, h Handler) error {
	cl.mu.Lock()
	cl.routes = append(cl.routes, route{filter, h})
	cl.pid++
	pid := cl.pid
	cl.mu.Unlock()
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, pid)
	body = append(body, str(filter)...)
	body = append(body, 0)
	if err := cl.write(&Packet{Type: SUBSCRIBE, Flags: 0x2, Body: body}); err != nil {
		return err
	}
	select {
	case <-cl.acks:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("mqtt: no SUBACK")
	}
}

func (cl *Client) Publish(topic string, payload []byte, retain bool) error {
	return cl.write(Publish(topic, payload, retain))
}

func (cl *Client) Close() error {
	cl.write(&Packet{Type: DISCONNECT})
	return cl.c.Close()
}
