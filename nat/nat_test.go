package nat

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestProbes(t *testing.T) {
	s := &Server{ServerIP: net.IPv4(203, 0, 113, 1), Port: 3074}
	from := &net.UDPAddr{IP: net.IPv4(198, 51, 100, 7), Port: 3074}
	out := s.handle([]byte{0x1e, 0x03, 0x00}, from)
	if out == nil || !bytes.Equal(out.data, []byte{31, 2, 0, 198, 51, 100, 7, 0x02, 0x0c}) {
		t.Fatalf("IP discovery %v", out)
	}
	out = s.handle([]byte{0x14, 0x02, 0x00, 0x03}, from)
	want := []byte{21, 2, 0, 198, 51, 100, 7, 0x02, 0x0c, 203, 0, 113, 1, 0x02, 0x0c}
	if out == nil || !bytes.Equal(out.data, want) || out.to != from {
		t.Fatalf("NAT discovery %x", out.data)
	}
	if s.handle([]byte{0x0e, 0x02, 0x00}, from) != nil {
		t.Fatal("keepalive answered")
	}
}

func intro(src, dst *net.UDPAddr) []byte {
	p := []byte{0x0A, 2, 0}
	p = append(p, bytes.Repeat([]byte{0x11}, 10)...) // id
	p = append(p, 1, 2, 3, 4)                        // hmac
	p = binary.LittleEndian.AppendUint16(append(p, src.IP.To4()...), uint16(src.Port))
	return binary.LittleEndian.AppendUint16(append(p, dst.IP.To4()...), uint16(dst.Port))
}

func TestIntroductions(t *testing.T) {
	joiner := &net.UDPAddr{IP: net.IPv4(198, 51, 100, 7), Port: 3074}
	host := &net.UDPAddr{IP: net.IPv4(198, 51, 100, 8), Port: 50000}
	stranger := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 80}
	known := map[string]bool{joiner.IP.String(): true, host.IP.String(): true}
	s := &Server{Known: func(ip net.IP) bool { return known[ip.String()] }, Log: slog.New(slog.DiscardHandler)}

	p := intro(joiner, host)
	out := s.handle(p, joiner)
	if out == nil || out.to.String() != host.String() || out.data[0] != 0x0B || !bytes.Equal(out.data[1:], p[1:]) {
		t.Fatalf("introduction %v", out)
	}
	if s.handle(intro(joiner, stranger), joiner) != nil {
		t.Fatal("introduced to an address that is not a player")
	}
	if s.handle(intro(stranger, host), stranger) != nil {
		t.Fatal("a non-player got an introduction")
	}
	empty := &net.UDPAddr{IP: net.IPv4(0, 255, 0, 255), Port: 0}
	if s.handle(intro(joiner, empty), joiner) != nil {
		t.Fatal("introduced to the empty slot")
	}
}

func TestRelayOffer(t *testing.T) {
	addr := make([]byte, 67)
	for slot := 1; slot <= 9; slot++ {
		copy(addr[slot*6:], []byte{0, 255, 0, 255, 0, 0})
	}
	copy(addr[60:], []byte{198, 51, 100, 8, 0x50, 0xC3}) // public 198.51.100.8:50000

	var off *Relay
	if got := off.Offer(addr, 1, 2, nil); !bytes.Equal(got, addr) {
		t.Fatal("a nil relay changed the address")
	}
	r := &Relay{IP: net.IPv4(203, 0, 113, 1), Low: 0, High: 0, Log: slog.New(slog.DiscardHandler)}
	if got := r.Offer(addr, 1, 2, nil); !bytes.Equal(got, addr) {
		t.Fatal("a relay with no ports changed the address")
	}

	// Find two free UDP ports for the test.
	c, _ := net.ListenUDP("udp4", &net.UDPAddr{})
	port := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	r = &Relay{IP: net.IPv4(127, 0, 0, 1), Low: port, High: port, Log: slog.New(slog.DiscardHandler),
		Allow: func(id uint64) bool { return id == 2 }}
	if got := r.Offer(addr, 1, 3, nil); !bytes.Equal(got, addr) {
		t.Fatal("relay offered to a player not listed")
	}
	got := r.Offer(addr, 1, 2, net.IPv4(127, 0, 0, 1))
	if bytes.Equal(got, addr) {
		t.Fatal("no relay offered")
	}
	if !bytes.Equal(got[6:12], binary.LittleEndian.AppendUint16([]byte{127, 0, 0, 1}, uint16(port))) {
		t.Fatalf("relay slot % x", got[6:12])
	}
	if !bytes.Equal(got[12:], addr[12:]) || !bytes.Equal(got[:6], addr[:6]) {
		t.Fatal("other slots changed")
	}
	if !bytes.Equal(addr[6:12], []byte{0, 255, 0, 255, 0, 0}) {
		t.Fatal("the host's own copy was modified")
	}
}

func TestRelayForwards(t *testing.T) {
	loop := net.IPv4(127, 0, 0, 1)
	host, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: loop})
	joiner, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: loop})
	defer host.Close()
	defer joiner.Close()
	host.SetReadDeadline(time.Now().Add(3 * time.Second))
	joiner.SetReadDeadline(time.Now().Add(3 * time.Second))
	c, _ := net.ListenUDP("udp4", &net.UDPAddr{})
	port := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()

	addr := make([]byte, 67)
	for slot := 1; slot <= 9; slot++ {
		copy(addr[slot*6:], []byte{0, 255, 0, 255, 0, 0})
	}
	hp := host.LocalAddr().(*net.UDPAddr).Port
	copy(addr[60:], binary.LittleEndian.AppendUint16([]byte{127, 0, 0, 1}, uint16(hp)))
	r := &Relay{IP: loop, Low: port, High: port, Log: slog.New(slog.DiscardHandler)}
	offered := r.Offer(addr, 1, 2, loop)
	relayAddr := &net.UDPAddr{IP: loop, Port: int(binary.LittleEndian.Uint16(offered[10:12]))}

	buf := make([]byte, 64)
	joiner.WriteToUDP([]byte("ping"), relayAddr)
	n, from, err := host.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "ping" || from.Port != relayAddr.Port {
		t.Fatalf("host got %q from %v (%v)", buf[:n], from, err)
	}
	host.WriteToUDP([]byte("pong"), from)
	n, _, err = joiner.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "pong" {
		t.Fatalf("joiner got %q (%v)", buf[:n], err)
	}
}
