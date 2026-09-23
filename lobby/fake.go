package lobby

import (
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/openpak/demonware/session"
)

// Fake registers an authenticated connection with no network behind it, from ip, so task
// handlers can be tested without a handshake. Pushes to it fail.
func (s *Server) Fake(sess *session.Session, ip string) *Conn {
	s.mu.Lock()
	if s.conns == nil {
		s.conns = map[uint64]*Conn{}
	}
	s.mu.Unlock()
	c := &Conn{srv: s, nc: fakeNet{ip: net.ParseIP(ip)}, ID: 0x0D3F00000000 + s.ids.Add(1), Session: sess}
	c.log = slog.New(slog.DiscardHandler)
	if s.Log != nil {
		c.log = s.Log
	}
	s.join(c)
	return c
}

// Drop closes a fake connection the way a real one closes.
func (s *Server) Drop(c *Conn) { s.leave(c) }

type fakeNet struct{ ip net.IP }

func (f fakeNet) Read([]byte) (int, error)         { return 0, errors.New("fake") }
func (f fakeNet) Write([]byte) (int, error)        { return 0, errors.New("fake") }
func (f fakeNet) Close() error                     { return nil }
func (f fakeNet) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (f fakeNet) RemoteAddr() net.Addr             { return &net.TCPAddr{IP: f.ip, Port: 40000} }
func (f fakeNet) SetDeadline(time.Time) error      { return nil }
func (f fakeNet) SetReadDeadline(time.Time) error  { return nil }
func (f fakeNet) SetWriteDeadline(time.Time) error { return nil }
