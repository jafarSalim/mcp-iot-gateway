// Command gateway is the MCP server. It speaks MCP on stdin/stdout and MQTT to the devices.
//
//	gateway                         embedded broker on 127.0.0.1:1883
//	gateway -broker 192.168.1.10:1883   use an existing broker (Mosquitto, EMQX, ...)
//	gateway -sim 3                  also start 3 simulated ESP32 nodes (no hardware needed)
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jafarSalim/mcp-iot-gateway/internal/gateway"
	"github.com/jafarSalim/mcp-iot-gateway/internal/mcp"
	"github.com/jafarSalim/mcp-iot-gateway/internal/mqtt"
	"github.com/jafarSalim/mcp-iot-gateway/internal/sim"
)

func main() {
	broker := flag.String("broker", "", "address of an existing MQTT broker; empty starts the embedded one")
	listen := flag.String("listen", "127.0.0.1:1883", "embedded broker address (use 0.0.0.0:1883 so real ESP32 boards can connect)")
	nsim := flag.Int("sim", 0, "number of simulated ESP32 nodes to start")
	flag.Parse()
	log.SetOutput(os.Stderr) // stdout belongs to the MCP protocol

	addr := *broker
	if addr == "" {
		b := mqtt.NewBroker()
		a, err := b.Listen(*listen)
		if err != nil {
			log.Fatalf("broker: %v", err)
		}
		addr = a
		log.Printf("embedded MQTT broker on %s", addr)
	}
	rooms := []string{"lab", "server room", "greenhouse", "office", "workshop"}
	for i := 0; i < *nsim; i++ {
		n := &sim.Node{ID: fmt.Sprintf("esp32-%02d", i+1), Name: fmt.Sprintf("Climate node %d", i+1), Location: rooms[i%len(rooms)], Base: 22 + float64(i)}
		if _, err := sim.Start(addr, n, 500*time.Millisecond, int64(i+1)); err != nil {
			log.Fatalf("sim: %v", err)
		}
	}
	cl, err := mqtt.Dial(addr, "mcp-gateway")
	if err != nil {
		log.Fatalf("mqtt: %v", err)
	}
	g, err := gateway.New(cl)
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}
	s := mcp.NewServer("mcp-iot-gateway", "0.1.0")
	g.Register(s)
	if err := s.Serve(os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
