// Package lobby is the Demonware lobby: the raw hello, the ticket-signed handshake, the
// encrypted channel, and remote-task dispatch (docs/protocol.md §3-4). What the tasks do is
// the caller's business: it installs a Handler.
package lobby

import (
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openpak/demonware/session"
	"github.com/openpak/demonware/wire"
)

// Request is one decoded remote task.
type Request struct {
	Service, Task byte
	Args          *wire.Reader // positioned after the task byte
	Raw           []byte       // the whole request payload, service byte included
}

// Response is a task's reply; Then runs after the reply has been sent (pushes that must
// follow it).
type Response struct {
	Body []byte
	Then func()
}

// Handler answers a task. A nil Body gets an empty success: every task must be answered.
type Handler func(c *Conn, r *Request) Response

// Server accepts lobby connections.
type Server struct {
	Sessions *session.Store
	Handle   Handler
	Log      *slog.Logger
	Idle     time.Duration // read timeout between frames; 0 is 30 minutes
	// PatchedKeys are public keys (DER SubjectPublicKeyInfo) a patched client carries in place
	// of the game's own; each is one more wrapped-key candidate in the handshake.
	PatchedKeys [][]byte

	ids     atomic.Uint64
	mu      sync.Mutex
	conns   map[uint64]*Conn
	onClose []func(*Conn)

	Accepted, Authenticated, Refused, Tasks atomic.Int64
}

// OnClose registers cleanup for a closing, authenticated connection.
func (s *Server) OnClose(f func(*Conn)) { s.onClose = append(s.onClose, f) }

// Serve accepts on ln until it fails.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.conns == nil {
		s.conns = map[uint64]*Conn{}
	}
	s.mu.Unlock()
	for {
		nc, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.Accepted.Add(1)
		c := &Conn{srv: s, nc: nc, ID: 0x0D3000000000 + s.ids.Add(1)}
		c.log = s.Log.With("conn", c.ID, "remote", nc.RemoteAddr().String())
		go c.run()
	}
}

// Each calls f for every authenticated connection.
func (s *Server) Each(f func(*Conn)) {
	s.mu.Lock()
	list := make([]*Conn, 0, len(s.conns))
	for _, c := range s.conns {
		list = append(list, c)
	}
	s.mu.Unlock()
	for _, c := range list {
		f(c)
	}
}

// Online is the number of authenticated connections.
func (s *Server) Online() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *Server) join(c *Conn) {
	s.mu.Lock()
	s.conns[c.ID] = c
	s.mu.Unlock()
}

func (s *Server) leave(c *Conn) {
	s.mu.Lock()
	_, was := s.conns[c.ID]
	delete(s.conns, c.ID)
	s.mu.Unlock()
	if was {
		for _, f := range s.onClose {
			f(c)
		}
	}
}
