package mqtt

import (
	"bufio"
	"bytes"
	"testing"
	"time"
)

func TestRemainingLengthRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 16383, 16384, 2097151} {
		p := &Packet{Type: PUBLISH, Body: make([]byte, n)}
		q, err := ReadPacket(bufio.NewReader(bytes.NewReader(p.Bytes())))
		if n > 1<<20 {
			if err == nil {
				t.Fatalf("expected size error for %d", n)
			}
			continue
		}
		if err != nil || len(q.Body) != n {
			t.Fatalf("n=%d err=%v", n, err)
		}
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		f, t string
		ok   bool
	}{
		{"devices/+/state", "devices/esp32-1/state", true},
		{"devices/+/state", "devices/esp32-1/sensors/t", false},
		{"devices/#", "devices/a/b/c", true},
		{"devices/a", "devices/a/b", false},
		{"#", "anything/at/all", true},
	}
	for _, c := range cases {
		if Match(c.f, c.t) != c.ok {
			t.Errorf("Match(%q,%q) != %v", c.f, c.t, c.ok)
		}
	}
}

func TestBrokerPubSubAndRetained(t *testing.T) {
	b := NewBroker()
	addr, err := b.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	pub, _ := Dial(addr, "pub")
	defer pub.Close()
	pub.Publish("devices/x/state", []byte(`{"online":true}`), true)
	time.Sleep(50 * time.Millisecond)

	got := make(chan string, 4)
	sub, _ := Dial(addr, "sub")
	defer sub.Close()
	if err := sub.Subscribe("devices/+/state", func(topic string, p []byte) { got <- topic + " " + string(p) }); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m != `devices/x/state {"online":true}` {
			t.Fatalf("retained: %s", m)
		}
	case <-time.After(time.Second):
		t.Fatal("no retained message")
	}
	pub.Publish("devices/y/state", []byte("hi"), false)
	select {
	case m := <-got:
		if m != "devices/y/state hi" {
			t.Fatalf("live: %s", m)
		}
	case <-time.After(time.Second):
		t.Fatal("no live message")
	}
}
