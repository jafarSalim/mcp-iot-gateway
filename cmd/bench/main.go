// Command bench measures the gateway: command round trips through MQTT, MCP calls over pipes,
// and broker fan-out throughput. Writes results/bench.json.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jafarSalim/mcp-iot-gateway/internal/gateway"
	"github.com/jafarSalim/mcp-iot-gateway/internal/mcp"
	"github.com/jafarSalim/mcp-iot-gateway/internal/mqtt"
	"github.com/jafarSalim/mcp-iot-gateway/internal/sim"
)

func pct(v []float64, p float64) float64 {
	s := append([]float64{}, v...)
	sort.Float64s(s)
	return s[int(p*float64(len(s)-1))]
}

func summary(v []float64) map[string]float64 {
	sum := 0.0
	for _, x := range v {
		sum += x
	}
	return map[string]float64{"p50": pct(v, .5), "p90": pct(v, .9), "p99": pct(v, .99), "mean": sum / float64(len(v)), "n": float64(len(v))}
}

func main() {
	b := mqtt.NewBroker()
	addr, _ := b.Listen("127.0.0.1:0")
	var nodes []*sim.Node
	for i := 1; i <= 5; i++ {
		n, _ := sim.Start(addr, &sim.Node{ID: fmt.Sprintf("esp32-%02d", i), Base: 22}, 100*time.Millisecond, int64(i))
		nodes = append(nodes, n)
	}
	cl, _ := mqtt.Dial(addr, "gw")
	g, _ := gateway.New(cl)
	time.Sleep(400 * time.Millisecond)
	out := map[string]any{}

	// 1. set_actuator round trip: gateway -> broker -> device -> broker -> gateway
	var rt []float64
	for i := 0; i < 2000; i++ {
		t0 := time.Now()
		if _, err := g.Set(fmt.Sprintf("esp32-%02d", i%5+1), "led", float64(i%256)); err != nil {
			panic(err)
		}
		rt = append(rt, float64(time.Since(t0).Microseconds())/1000)
	}
	out["set_actuator_ms"] = summary(rt)
	out["set_actuator_samples"] = rt

	// 2. full MCP tools/call over pipes (JSON-RPC encode/decode included)
	s := mcp.NewServer("bench", "0")
	g.Register(s)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go s.Serve(inR, outW)
	rd := bufio.NewReader(outR)
	var readMs, setMs []float64
	for i := 0; i < 2000; i++ {
		for _, kind := range []string{"read", "set"} {
			var line string
			if kind == "read" {
				line = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"read_sensor","arguments":{"device_id":"esp32-01","sensor":"temperature","last_n":60}}}`, i)
			} else {
				line = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"set_actuator","arguments":{"device_id":"esp32-03","actuator":"fan","value":%v}}}`, i, i%2 == 0)
			}
			t0 := time.Now()
			inW.Write([]byte(line + "\n"))
			resp, _ := rd.ReadBytes('\n')
			ms := float64(time.Since(t0).Microseconds()) / 1000
			var m map[string]any
			json.Unmarshal(resp, &m)
			if m["result"].(map[string]any)["isError"] == true {
				panic(string(resp))
			}
			if kind == "read" {
				readMs = append(readMs, ms)
			} else {
				setMs = append(setMs, ms)
			}
		}
	}
	out["mcp_read_sensor_ms"] = summary(readMs)
	out["mcp_set_actuator_ms"] = summary(setMs)
	for _, n := range nodes {
		n.Stop()
	}

	// 3. broker fan-out: K publishers flooding telemetry, one subscriber on devices/+/telemetry
	var tp []map[string]float64
	for _, k := range []int{1, 10, 50, 100} {
		var got atomic.Int64
		sub, _ := mqtt.Dial(addr, fmt.Sprintf("sub-%d", k))
		sub.Subscribe(fmt.Sprintf("flood%d/+/telemetry", k), func(string, []byte) { got.Add(1) })
		var wg sync.WaitGroup
		stop := time.Now().Add(2 * time.Second)
		for p := 0; p < k; p++ {
			wg.Add(1)
			go func(p int) {
				defer wg.Done()
				c, _ := mqtt.Dial(addr, fmt.Sprintf("pub-%d-%d", k, p))
				payload := []byte(`{"temperature":23.41,"humidity":48.2,"fan":false}`)
				topic := fmt.Sprintf("flood%d/dev%d/telemetry", k, p)
				for time.Now().Before(stop) {
					c.Publish(topic, payload, false)
				}
				c.Close()
			}(p)
		}
		wg.Wait()
		time.Sleep(300 * time.Millisecond)
		tp = append(tp, map[string]float64{"publishers": float64(k), "delivered_per_s": float64(got.Load()) / 2})
		sub.Close()
	}
	out["throughput"] = tp
	f, _ := os.Create("results/bench.json")
	json.NewEncoder(f).Encode(out)
	f.Close()
	for _, k := range []string{"set_actuator_ms", "mcp_read_sensor_ms", "mcp_set_actuator_ms"} {
		fmt.Println(k, out[k])
	}
	fmt.Println(tp)
}
