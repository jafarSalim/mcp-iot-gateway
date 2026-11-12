package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jafarSalim/mcp-iot-gateway/internal/mcp"
	"github.com/jafarSalim/mcp-iot-gateway/internal/mqtt"
	"github.com/jafarSalim/mcp-iot-gateway/internal/sim"
)

func setup(t *testing.T) (*Gateway, func()) {
	b := mqtt.NewBroker()
	addr, err := b.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var nodes []*sim.Node
	for i := 1; i <= 2; i++ {
		n, err := sim.Start(addr, &sim.Node{ID: fmt.Sprintf("esp32-%02d", i), Base: 22}, 20*time.Millisecond, int64(i))
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
	}
	cl, _ := mqtt.Dial(addr, "gw")
	g, err := New(cl)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	return g, func() {
		for _, n := range nodes {
			n.Stop()
		}
		cl.Close()
		b.Close()
	}
}

func TestDiscoveryReadAndActuate(t *testing.T) {
	g, done := setup(t)
	defer done()
	ds := g.ListDevices()
	if len(ds) != 2 || !ds[0].Online || ds[0].Sensors["temperature"].Unit != "°C" {
		t.Fatalf("discovery: %+v", ds)
	}
	r, err := g.Read("esp32-01", "temperature", 3)
	if err != nil || r["window"].(map[string]any)["samples"].(int) != 3 {
		t.Fatalf("read: %v %v", r, err)
	}
	s, err := g.Set("esp32-01", "fan", true)
	if err != nil || s["confirmed"] != true {
		t.Fatalf("set: %v %v", s, err)
	}
	if _, err := g.Set("esp32-01", "led", 300.0); err == nil || !strings.Contains(err.Error(), "between 0 and 255") {
		t.Fatalf("range check: %v", err)
	}
	if _, err := g.Set("esp32-01", "fan", "on"); err == nil {
		t.Fatal("type check missing")
	}
	if _, err := g.Read("esp32-01", "pressure", 0); err == nil || !strings.Contains(err.Error(), "humidity, temperature") {
		t.Fatalf("unknown sensor message: %v", err)
	}
}

func TestOfflineDeviceTimesOut(t *testing.T) {
	g, done := setup(t)
	defer done()
	g.mu.Lock()
	g.devices["ghost"] = &device{state: State{Actuators: map[string]Capability{"fan": {Type: "bool"}}}, history: map[string][]sample{}, actual: map[string]any{}}
	g.mu.Unlock()
	g.Timeout = 100 * time.Millisecond
	if _, err := g.Set("ghost", "fan", true); err == nil || !strings.Contains(err.Error(), "no acknowledgement") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestMCPProtocolOverPipes(t *testing.T) {
	g, done := setup(t)
	defer done()
	s := mcp.NewServer("test", "0")
	g.Register(s)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go s.Serve(inR, outW)
	rd := bufio.NewReader(outR)
	call := func(line string) map[string]any {
		inW.Write([]byte(line + "\n"))
		b, _ := rd.ReadBytes('\n')
		var m map[string]any
		json.Unmarshal(b, &m)
		return m
	}
	init := call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	if init["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("init: %v", init)
	}
	inW.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	list := call(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if n := len(list["result"].(map[string]any)["tools"].([]any)); n != 3 {
		t.Fatalf("tools: %d", n)
	}
	res := call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"set_actuator","arguments":{"device_id":"esp32-02","actuator":"led","value":128}}}`)
	if res["result"].(map[string]any)["isError"] != false {
		t.Fatalf("call: %v", res)
	}
	bad := call(`{"jsonrpc":"2.0","id":4,"method":"nope"}`)
	if bad["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown method: %v", bad)
	}
	inW.Close()
}
