package alpaca

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func reuseListen(t *testing.T, addr string) net.PacketConn {
	t.Helper()
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = setSocketReuse(fd) }); err != nil {
			return err
		}
		return serr
	}}
	conn, err := lc.ListenPacket(context.Background(), "udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestDiscoveryAnswersWithAlpacaPort(t *testing.T) {
	conn := reuseListen(t, "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveDiscovery(ctx, conn, 11112)

	client, err := net.Dial("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("alpacadiscovery1")); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("no discovery reply: %v", err)
	}
	var reply struct{ AlpacaPort int }
	if err := json.Unmarshal(buf[:n], &reply); err != nil {
		t.Fatalf("reply %q: %v", buf[:n], err)
	}
	if reply.AlpacaPort != 11112 {
		t.Errorf("AlpacaPort = %d, want 11112", reply.AlpacaPort)
	}
}

func TestDiscoveryIgnoresJunk(t *testing.T) {
	conn := reuseListen(t, "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveDiscovery(ctx, conn, 11112)

	client, err := net.Dial("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.Write([]byte("definitely not alpaca"))
	client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, err := client.Read(make([]byte, 256)); err == nil {
		t.Errorf("junk packet got a %d-byte reply", n)
	}
}

// TestSharedBind proves the SO_REUSEADDR point of the whole exercise: two
// sockets (alpaca-switch's responder and skyq's) can hold the same UDP port
// at once. Broadcast delivery to both is verified out-of-process — Windows
// hands broadcast datagrams to every socket bound to the port.
func TestSharedBind(t *testing.T) {
	first := reuseListen(t, "0.0.0.0:0")
	port := first.LocalAddr().(*net.UDPAddr).Port
	reuseListen(t, "0.0.0.0:"+strconv.Itoa(port)) // Fatals if the bind fails
}

// Guard against the responder replying to off-LAN sources.
func TestDiscoveryLocalOnly(t *testing.T) {
	l := &localNetworks{fetched: time.Now()}
	_, n, _ := net.ParseCIDR("192.168.1.0/24")
	l.nets = []*net.IPNet{n}
	if l.contains(net.ParseIP("8.8.8.8")) {
		t.Error("public IP accepted")
	}
	if !l.contains(net.ParseIP("127.0.0.1")) {
		t.Error("loopback rejected")
	}
	if !l.contains(net.ParseIP("192.168.1.50")) {
		t.Error("LAN IP rejected")
	}
}
