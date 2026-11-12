// Package sim runs simulated ESP32 nodes that speak exactly the same MQTT protocol as the real firmware
// in firmware/esp32_node: a retained state announcement, periodic telemetry, and acknowledged commands.
package sim

import (
	"encoding/json"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/jafarSalim/mcp-iot-gateway/internal/mqtt"
)

type Node struct {
	ID, Name, Location string
	Base               float64 // mean temperature
	cl                 *mqtt.Client
	mu                 sync.Mutex
	fan                bool
	led                float64
	temp, hum          float64
	stop               chan struct{}
	rng                *rand.Rand
}

func f(v float64) *float64 { return &v }

func (n *Node) state() []byte {
	b, _ := json.Marshal(map[string]any{
		"id": n.ID, "name": n.Name, "model": "ESP32-WROOM-32 (simulated)", "location": n.Location,
		"sensors":   map[string]any{"temperature": map[string]any{"unit": "°C"}, "humidity": map[string]any{"unit": "%RH"}},
		"actuators": map[string]any{"fan": map[string]any{"type": "bool"}, "led": map[string]any{"type": "int", "min": 0, "max": 255}},
	})
	return b
}

// Start connects to the broker and begins publishing every period.
func Start(addr string, n *Node, period time.Duration, seed int64) (*Node, error) {
	cl, err := mqtt.Dial(addr, n.ID)
	if err != nil {
		return nil, err
	}
	n.cl, n.stop, n.rng = cl, make(chan struct{}), rand.New(rand.NewSource(seed))
	n.temp, n.hum = n.Base, 50
	if err := cl.Subscribe("devices/"+n.ID+"/cmd", n.onCmd); err != nil {
		return nil, err
	}
	cl.Publish("devices/"+n.ID+"/state", n.state(), true)
	go n.loop(period)
	return n, nil
}

func (n *Node) loop(period time.Duration) {
	t := time.NewTicker(period)
	defer t.Stop()
	start := time.Now()
	for {
		select {
		case <-n.stop:
			return
		case now := <-t.C:
			n.mu.Lock()
			phase := 2 * math.Pi * now.Sub(start).Seconds() / 120 // a compressed "day" of two minutes
			target := n.Base + 3*math.Sin(phase)
			if n.fan {
				target -= 2.5
			}
			n.temp += 0.2*(target-n.temp) + 0.08*n.rng.NormFloat64()
			n.hum = 50 - 6*math.Sin(phase) + 0.4*n.rng.NormFloat64()
			msg, _ := json.Marshal(map[string]any{"temperature": math.Round(n.temp*100) / 100, "humidity": math.Round(n.hum*10) / 10, "fan": n.fan})
			n.mu.Unlock()
			n.cl.Publish("devices/"+n.ID+"/telemetry", msg, false)
		}
	}
}

func (n *Node) onCmd(_ string, p []byte) {
	var c struct {
		ID       string `json:"id"`
		Actuator string `json:"actuator"`
		Value    any    `json:"value"`
	}
	reply := map[string]any{"ok": false}
	if json.Unmarshal(p, &c) == nil {
		reply["id"], reply["actuator"], reply["value"] = c.ID, c.Actuator, c.Value
		n.mu.Lock()
		switch c.Actuator {
		case "fan":
			if v, ok := c.Value.(bool); ok {
				n.fan, reply["ok"] = v, true
			}
		case "led":
			if v, ok := c.Value.(float64); ok && v >= 0 && v <= 255 {
				n.led, reply["ok"] = v, true
			}
		}
		n.mu.Unlock()
		if reply["ok"] == false {
			reply["error"] = "unsupported actuator or value"
		}
	}
	b, _ := json.Marshal(reply)
	n.cl.Publish("devices/"+n.ID+"/ack", b, false)
}

func (n *Node) Stop() {
	close(n.stop)
	n.cl.Close()
}
