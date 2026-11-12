// Package gateway keeps a live registry of MQTT devices and exposes them to an AI model as MCP tools.
package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jafarSalim/mcp-iot-gateway/internal/mcp"
	"github.com/jafarSalim/mcp-iot-gateway/internal/mqtt"
)

// Capability describes one sensor or actuator a device announces about itself.
type Capability struct {
	Unit string   `json:"unit,omitempty"`
	Type string   `json:"type,omitempty"` // actuators: "bool" or "int"
	Min  *float64 `json:"min,omitempty"`
	Max  *float64 `json:"max,omitempty"`
}

// State is the retained announcement a device publishes on devices/{id}/state.
type State struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Model     string                `json:"model"`
	Location  string                `json:"location"`
	Sensors   map[string]Capability `json:"sensors"`
	Actuators map[string]Capability `json:"actuators"`
}

type sample struct {
	t time.Time
	v float64
}

type device struct {
	state    State
	lastSeen time.Time
	history  map[string][]sample // ring buffers, newest last
	actual   map[string]any
}

type Gateway struct {
	mu      sync.RWMutex
	devices map[string]*device
	cl      *mqtt.Client
	pending sync.Map // correlation id -> chan ack
	MaxHist int
	Timeout time.Duration
}

type ack struct {
	ID       string `json:"id"`
	Actuator string `json:"actuator"`
	Value    any    `json:"value"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

func New(cl *mqtt.Client) (*Gateway, error) {
	g := &Gateway{devices: map[string]*device{}, cl: cl, MaxHist: 720, Timeout: 3 * time.Second}
	subs := map[string]mqtt.Handler{
		"devices/+/state":     g.onState,
		"devices/+/telemetry": g.onTelemetry,
		"devices/+/ack":       g.onAck,
	}
	for f, h := range subs {
		if err := cl.Subscribe(f, h); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func idOf(topic string) string { return strings.Split(topic, "/")[1] }

func (g *Gateway) dev(id string) *device {
	d := g.devices[id]
	if d == nil {
		d = &device{history: map[string][]sample{}, actual: map[string]any{}}
		g.devices[id] = d
	}
	return d
}

func (g *Gateway) onState(topic string, p []byte) {
	var s State
	if json.Unmarshal(p, &s) != nil {
		return
	}
	g.mu.Lock()
	d := g.dev(idOf(topic))
	d.state, d.lastSeen = s, time.Now()
	g.mu.Unlock()
}

func (g *Gateway) onTelemetry(topic string, p []byte) {
	var m map[string]any
	if json.Unmarshal(p, &m) != nil {
		return
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	d := g.dev(idOf(topic))
	d.lastSeen = now
	for k, v := range m {
		switch x := v.(type) {
		case float64:
			h := append(d.history[k], sample{now, x})
			if len(h) > g.MaxHist {
				h = h[len(h)-g.MaxHist:]
			}
			d.history[k] = h
		case bool:
			d.actual[k] = x
		}
	}
}

func (g *Gateway) onAck(topic string, p []byte) {
	var a ack
	if json.Unmarshal(p, &a) != nil {
		return
	}
	if ch, ok := g.pending.Load(a.ID); ok {
		ch.(chan ack) <- a
	}
	if a.OK {
		g.mu.Lock()
		g.dev(idOf(topic)).actual[a.Actuator] = a.Value
		g.mu.Unlock()
	}
}

// ---- tool implementations ----

type deviceView struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Model     string                `json:"model"`
	Location  string                `json:"location"`
	Online    bool                  `json:"online"`
	LastSeenS float64               `json:"last_seen_seconds_ago"`
	Sensors   map[string]Capability `json:"sensors"`
	Actuators map[string]Capability `json:"actuators"`
	State     map[string]any        `json:"actuator_state,omitempty"`
}

func (g *Gateway) ListDevices() []deviceView {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []deviceView
	for id, d := range g.devices {
		age := time.Since(d.lastSeen).Seconds()
		st := map[string]any{}
		for k, v := range d.actual {
			st[k] = v
		}
		out = append(out, deviceView{id, d.state.Name, d.state.Model, d.state.Location, age < 30, math.Round(age*10) / 10, d.state.Sensors, d.state.Actuators, st})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (g *Gateway) Read(id, sensor string, lastN int) (map[string]any, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	d, ok := g.devices[id]
	if !ok {
		return nil, fmt.Errorf("unknown device %q; call list_devices to see what is connected", id)
	}
	capb, ok := d.state.Sensors[sensor]
	if !ok {
		return nil, fmt.Errorf("device %q has no sensor %q (it has: %s)", id, sensor, keys(d.state.Sensors))
	}
	h := d.history[sensor]
	if len(h) == 0 {
		return nil, fmt.Errorf("no readings yet from %s/%s", id, sensor)
	}
	last := h[len(h)-1]
	out := map[string]any{"device": id, "sensor": sensor, "unit": capb.Unit, "value": last.v, "age_seconds": math.Round(time.Since(last.t).Seconds()*10) / 10}
	if lastN > 1 {
		if lastN > len(h) {
			lastN = len(h)
		}
		w := h[len(h)-lastN:]
		mn, mx, sum := math.Inf(1), math.Inf(-1), 0.0
		vals := make([]float64, len(w))
		for i, s := range w {
			mn, mx, sum = math.Min(mn, s.v), math.Max(mx, s.v), sum+s.v
			vals[i] = math.Round(s.v*100) / 100
		}
		out["window"] = map[string]any{"samples": len(w), "min": mn, "max": mx, "mean": math.Round(sum/float64(len(w))*100) / 100,
			"span_seconds": math.Round(w[len(w)-1].t.Sub(w[0].t).Seconds()), "values": vals}
	}
	return out, nil
}

func (g *Gateway) Set(id, actuator string, value any) (map[string]any, error) {
	g.mu.RLock()
	d, ok := g.devices[id]
	var capb Capability
	if ok {
		capb, ok = d.state.Actuators[actuator]
	}
	g.mu.RUnlock()
	if d == nil {
		return nil, fmt.Errorf("unknown device %q; call list_devices to see what is connected", id)
	}
	if !ok {
		return nil, fmt.Errorf("device %q has no actuator %q (it has: %s)", id, actuator, keys(d.state.Actuators))
	}
	// Validate against what the device itself declared, before anything is sent.
	switch capb.Type {
	case "bool":
		if _, isBool := value.(bool); !isBool {
			return nil, fmt.Errorf("%s.%s takes true or false", id, actuator)
		}
	case "int":
		f, isNum := value.(float64)
		if !isNum || f != math.Trunc(f) {
			return nil, fmt.Errorf("%s.%s takes a whole number", id, actuator)
		}
		if (capb.Min != nil && f < *capb.Min) || (capb.Max != nil && f > *capb.Max) {
			return nil, fmt.Errorf("%s.%s must be between %v and %v", id, actuator, *capb.Min, *capb.Max)
		}
	}
	b := make([]byte, 6)
	rand.Read(b)
	corr := hex.EncodeToString(b)
	ch := make(chan ack, 1)
	g.pending.Store(corr, ch)
	defer g.pending.Delete(corr)
	t0 := time.Now()
	cmd, _ := json.Marshal(map[string]any{"id": corr, "actuator": actuator, "value": value})
	if err := g.cl.Publish("devices/"+id+"/cmd", cmd, false); err != nil {
		return nil, err
	}
	select {
	case a := <-ch:
		if !a.OK {
			return nil, fmt.Errorf("device refused: %s", a.Error)
		}
		return map[string]any{"device": id, "actuator": actuator, "value": a.Value, "confirmed": true,
			"round_trip_ms": math.Round(float64(time.Since(t0).Microseconds())/10) / 100}, nil
	case <-time.After(g.Timeout):
		return nil, fmt.Errorf("no acknowledgement from %s within %v; the device may be offline", id, g.Timeout)
	}
}

func keys[V any](m map[string]V) string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return strings.Join(k, ", ")
}

func js(v any) string {
	b, _ := json.MarshalIndent(v, "", " ")
	return string(b)
}

// Register adds the four tools to an MCP server.
func (g *Gateway) Register(s *mcp.Server) {
	s.Instructions = "Tools for real IoT devices on an MQTT network. Call list_devices first. set_actuator changes the physical world " +
		"(fans, relays, LEDs): confirm with the user before switching anything that could be unsafe, and report the confirmed value."
	s.AddTool(mcp.Tool{Name: "list_devices", Title: "List devices",
		Description: "All devices on the MQTT network, with their sensors, actuators, current actuator state and whether they are online.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Annotations: map[string]any{"readOnlyHint": true},
		Handler:     func(json.RawMessage) (string, error) { return js(g.ListDevices()), nil }})
	s.AddTool(mcp.Tool{Name: "read_sensor", Title: "Read a sensor",
		Description: "Latest value of one sensor, and optionally min/mean/max over the last N samples.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"device_id":{"type":"string"},"sensor":{"type":"string"},"last_n":{"type":"integer","minimum":1,"maximum":720,"description":"include stats over the last N samples"}},"required":["device_id","sensor"]}`),
		Annotations: map[string]any{"readOnlyHint": true},
		Handler: func(a json.RawMessage) (string, error) {
			var p struct {
				DeviceID string `json:"device_id"`
				Sensor   string `json:"sensor"`
				LastN    int    `json:"last_n"`
			}
			if err := json.Unmarshal(a, &p); err != nil {
				return "", err
			}
			r, err := g.Read(p.DeviceID, p.Sensor, p.LastN)
			if err != nil {
				return "", err
			}
			return js(r), nil
		}})
	s.AddTool(mcp.Tool{Name: "set_actuator", Title: "Switch an actuator",
		Description: "Send a command to a device actuator (for example fan=true or led=128) and wait for the device to confirm it. Values are checked against the range the device declares.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"device_id":{"type":"string"},"actuator":{"type":"string"},"value":{"description":"true/false or a whole number"}},"required":["device_id","actuator","value"]}`),
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true},
		Handler: func(a json.RawMessage) (string, error) {
			var p struct {
				DeviceID string `json:"device_id"`
				Actuator string `json:"actuator"`
				Value    any    `json:"value"`
			}
			if err := json.Unmarshal(a, &p); err != nil {
				return "", err
			}
			r, err := g.Set(p.DeviceID, p.Actuator, p.Value)
			if err != nil {
				return "", err
			}
			return js(r), nil
		}})
}
