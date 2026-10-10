// Package nat answers the games' UDP 3074 traffic: the NAT probes that tell a console its
// public address and NAT type, and the introductions that let a joiner reach a host
// (docs/protocol.md §5). It also runs the optional per-pair UDP relay.
package nat

import (
	"encoding/binary"
	"log/slog"
	"net"
	"sync/atomic"
)

// Known decides whether an address belongs to a player connected to the lobby. Probes are
// answered for anyone (the game probes before it opens the lobby); introductions only run
// between connected players, so the server cannot be used to bounce packets elsewhere.
type Known func(ip net.IP) bool

// Server is the UDP 3074 responder.
type Server struct {
	Conn     *net.UDPConn
	ServerIP net.IP // announced in NAT discovery replies
	Port     int
	Known    Known
	Log      *slog.Logger

	Probes, Intros, Denied atomic.Int64
}

const introSize = 29

// Serve reads until the socket is closed.
func (s *Server) Serve() error {
	buf := make([]byte, 2048)
	for {
		n, from, err := s.Conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		if from.IP.To4() == nil || n < 3 {
			continue
		}
		if reply := s.handle(buf[:n], from); reply != nil {
			_, _ = s.Conn.WriteToUDP(reply.data, reply.to)
		}
	}
}

type out struct {
	data []byte
	to   *net.UDPAddr
}

func addr(ip net.IP, port int) []byte {
	return binary.LittleEndian.AppendUint16(append([]byte(nil), ip.To4()...), uint16(port))
}

func slotAddr(b []byte) *net.UDPAddr {
	ip := net.IPv4(b[0], b[1], b[2], b[3])
	port := int(binary.LittleEndian.Uint16(b[4:6]))
	if port == 0 || (b[0] == 0 && b[1] == 255 && b[2] == 0 && b[3] == 255) {
		return nil
	}
	return &net.UDPAddr{IP: ip, Port: port}
}

func (s *Server) handle(p []byte, from *net.UDPAddr) *out {
	switch {
	case p[0] == 30: // IP discovery
		s.Probes.Add(1)
		return &out{append([]byte{31, 2, 0}, addr(from.IP, from.Port)...), from}
	case p[0] == 20: // NAT discovery
		s.Probes.Add(1)
		r := append([]byte{21, 2, 0}, addr(from.IP, from.Port)...)
		return &out{append(r, addr(s.ServerIP, s.Port)...), from}
	case p[0] == 0x0A && len(p) == introSize:
		dst := slotAddr(p[23:29])
		if dst == nil || dst.IP.To4() == nil || (s.Known != nil && (!s.Known(dst.IP) || !s.Known(from.IP))) {
			s.Denied.Add(1)
			return nil
		}
		s.Intros.Add(1)
		fwd := append([]byte{0x0B}, p[1:]...)
		if s.Log != nil {
			s.Log.Debug("nat: introduction")
		}
		return &out{fwd, dst}
	}
	return nil // 0x0E keepalives and anything unknown
}
