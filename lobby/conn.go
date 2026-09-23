package lobby

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/openpak/demonware/account"
	"github.com/openpak/demonware/session"
	"github.com/openpak/demonware/wire"
)

const (
	helloSize     = 28
	maxFrame      = 1 << 21
	handshakeTime = 30 * time.Second
)

// Conn is one lobby connection.
type Conn struct {
	ID      uint64 // sent in 0x81; the game surfaces it as its connected id
	Session *session.Session

	srv *Server
	nc  net.Conn
	log *slog.Logger

	maxLen uint32
	nonce  [8]byte
	hello  []byte // the 0x81 payload, for the transcript

	sendMu sync.Mutex
	ch     *channel
	closed sync.Once
}

// Player is the connection's proven account.
func (c *Conn) Player() *account.Player { return &c.Session.Player }

// Title is the Demonware title id the player logged in to.
func (c *Conn) Title() uint32 { return c.Session.Title }

// OnlineID is the player's Demonware id.
func (c *Conn) OnlineID() uint64 { return c.Session.Player.OnlineID() }

// Log is the connection's logger.
func (c *Conn) Log() *slog.Logger { return c.log }

// RemoteIP is the peer's address.
func (c *Conn) RemoteIP() net.IP {
	if a, ok := c.nc.RemoteAddr().(*net.TCPAddr); ok {
		return a.IP
	}
	return nil
}

// Close ends the connection.
func (c *Conn) Close() { c.closed.Do(func() { c.nc.Close() }) }

func (c *Conn) write(b []byte) error {
	_ = c.nc.SetWriteDeadline(time.Now().Add(15 * time.Second))
	_, err := c.nc.Write(b)
	return err
}

func frame(body ...byte) []byte {
	f := binary.LittleEndian.AppendUint32(nil, uint32(1+len(body)))
	return append(append(f, frameMark), body...)
}

// Push sends a pushed message (docs/protocol.md §3).
func (c *Conn) Push(payload []byte) error { return c.sendSealed(innerPush, payload) }

func (c *Conn) sendSealed(inner byte, payload []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.ch == nil {
		return errors.New("lobby: not authenticated")
	}
	f, err := c.ch.seal(inner, payload)
	if err != nil {
		return err
	}
	return c.write(f)
}

// readFrame returns a whole frame, length prefix included; nil for a keepalive.
func (c *Conn) readFrame(deadline time.Duration) ([]byte, error) {
	_ = c.nc.SetReadDeadline(time.Now().Add(deadline))
	var hdr [4]byte
	if _, err := io.ReadFull(c.nc, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n == 0 {
		return nil, nil
	}
	if n < 2 || n > c.maxLen || n > maxFrame {
		return nil, fmt.Errorf("frame of %d bytes", n)
	}
	f := make([]byte, 4+n)
	copy(f, hdr[:])
	if _, err := io.ReadFull(c.nc, f[4:]); err != nil {
		return nil, err
	}
	if f[4] != frameMark {
		return nil, fmt.Errorf("frame mark %#02x", f[4])
	}
	return f, nil
}

func (c *Conn) run() {
	defer c.Close()
	if err := c.handshake(); err != nil {
		c.srv.Refused.Add(1)
		c.log.Info("lobby handshake refused", "err", err)
		return
	}
	c.srv.Authenticated.Add(1)
	c.srv.join(c)
	defer c.srv.leave(c)
	c.log = c.log.With("pid", c.Session.Player.PID, "title", c.Session.Title)
	c.log.Info("lobby connected", "nickname", c.Session.Player.Nickname)

	idle := c.srv.Idle
	if idle == 0 {
		idle = 30 * time.Minute
	}
	for {
		f, err := c.readFrame(idle)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				c.log.Info("lobby closed", "err", err)
			} else {
				c.log.Info("lobby closed")
			}
			return
		}
		if f == nil {
			c.sendMu.Lock()
			err := c.write(make([]byte, 4)) // keepalive back
			c.sendMu.Unlock()
			if err != nil {
				return
			}
			continue
		}
		if f[5] != msgSealed {
			c.log.Warn("lobby: unexpected clear message", "type", f[5])
			continue
		}
		inner, payload, err := c.ch.open(f)
		if err != nil {
			c.log.Warn("lobby: dropped frame", "err", err)
			continue
		}
		if inner != innerTask {
			c.log.Debug("lobby: inner message ignored", "type", inner, "len", len(payload))
			continue
		}
		if err := c.task(payload); err != nil {
			c.log.Info("lobby: send failed", "err", err)
			return
		}
	}
}

func (c *Conn) handshake() error {
	_ = c.nc.SetReadDeadline(time.Now().Add(handshakeTime))
	hello := make([]byte, helloSize)
	if _, err := io.ReadFull(c.nc, hello); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	le := binary.LittleEndian
	if le.Uint32(hello[0:]) != 200 || le.Uint32(hello[4:]) != 200 ||
		le.Uint32(hello[8:]) != versionLow || le.Uint32(hello[12:]) != versionHigh {
		return fmt.Errorf("hello: unexpected versions % x", hello[:16])
	}
	c.maxLen = le.Uint32(hello[16:])
	copy(c.nonce[:], hello[20:28])

	c.hello = le.AppendUint32(nil, versionHigh)
	c.hello = le.AppendUint64(c.hello, c.ID)
	c.hello = append(c.hello, make([]byte, 8)...)
	_, _ = rand.Read(c.hello[12:])
	if err := c.write(frame(append([]byte{msgHello}, c.hello...)...)); err != nil {
		return err
	}

	var f []byte
	for f == nil { // keepalives may come first
		var err error
		if f, err = c.readFrame(handshakeTime); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if f[5] != msgAuth || len(f) < 6+8 {
		return fmt.Errorf("auth: message %#02x", f[5])
	}
	ch, sess, err := c.authenticate(f)
	if err != nil {
		c.write(frame(append([]byte{msgAuthFail}, le.AppendUint32(nil, 1)...)...))
		return err
	}
	c.ch, c.Session = ch, sess
	return c.write(frame(append([]byte{msgAuthOK}, ch.check[:]...)...))
}

// authenticate finds the ticket in the 0x82, requires it to be one we issued, and checks the
// client's tag with that ticket's key (plain, then the wrapped variants).
func (c *Conn) authenticate(f []byte) (*channel, *session.Session, error) {
	ticket := locateTicket(f[6 : len(f)-8])
	if ticket == nil {
		return nil, nil, errors.New("no ticket in 0x82")
	}
	sess := c.srv.Sessions.Find(ticket)
	if sess == nil {
		return nil, nil, errors.New("ticket not issued here, or expired")
	}
	le := binary.LittleEndian
	var t bytes.Buffer
	t.Write(le.AppendUint32(nil, versionLow))
	t.Write(le.AppendUint32(nil, versionHigh))
	t.Write(le.AppendUint32(nil, c.maxLen))
	t.Write(c.nonce[:])
	t.Write(le.AppendUint32(nil, uint32(len(c.hello)+2)))
	t.Write([]byte{frameMark, msgHello})
	t.Write(c.hello)
	t.Write(f[:len(f)-8])
	tag := f[len(f)-8:]
	keys := [][]byte{sess.LobbyKey(), wrapped(sess.LobbyKey())}
	for _, der := range c.srv.PatchedKeys {
		keys = append(keys, wrappedWith(sess.LobbyKey(), der))
	}
	for _, key := range keys {
		ch, err := derive(t.Bytes(), key)
		if err != nil {
			return nil, nil, err
		}
		if hmac.Equal(ch.tag[:], tag) {
			return ch, sess, nil
		}
	}
	return nil, nil, errors.New("tag does not match the ticket's key")
}

// locateTicket looks for the ticket magic at every bit offset of b, in both bit orders (the
// fields before it are bit-packed), and returns the 128 bytes starting there.
func locateTicket(b []byte) []byte {
	magic := binary.LittleEndian.AppendUint32(nil, session.Magic)
	for _, msbFirst := range []bool{false, true} {
		bit := func(i int) byte {
			if msbFirst {
				return b[i>>3] >> (7 - uint(i&7)) & 1
			}
			return b[i>>3] >> uint(i&7) & 1
		}
		byteAt := func(start int) byte {
			var v byte
			for j := 0; j < 8; j++ {
				if msbFirst {
					v = v<<1 | bit(start+j)
				} else {
					v |= bit(start+j) << uint(j)
				}
			}
			return v
		}
		for off := 0; off+session.Size*8 <= len(b)*8; off++ {
			match := true
			for k := 0; k < 4 && match; k++ {
				match = byteAt(off+8*k) == magic[k]
			}
			if !match {
				continue
			}
			out := make([]byte, session.Size)
			for k := range out {
				out[k] = byteAt(off + 8*k)
			}
			return out
		}
	}
	return nil
}

// task dispatches one request and sends its reply.
func (c *Conn) task(p []byte) error {
	c.srv.Tasks.Add(1)
	req := &Request{Raw: p}
	var body []byte
	var then func()
	if len(p) < 3 {
		c.log.Warn("lobby: task too short", "payload", fmt.Sprintf("%x", p))
		body = wire.Fail(wire.Failed)
	} else {
		req.Service = p[0]
		req.Args = wire.NewReader(p)
		req.Args.Off = 1
		task, err := req.Args.U8()
		if err != nil {
			// Answer anyway: an unanswered task fails the game's whole login sequence.
			task = p[2]
			body = wire.Empty(task)
			c.log.Warn("lobby: task id unreadable", "service", req.Service, "payload", fmt.Sprintf("%x", p))
		}
		req.Task = task
		if body == nil {
			resp := c.srv.Handle(c, req)
			body, then = resp.Body, resp.Then
			if body == nil {
				body = wire.Empty(task)
			}
		}
		c.log.Debug("task", "service", req.Service, "task", req.Task, "args", fmt.Sprintf("%x", p[3:]))
	}
	if err := c.sendSealed(innerReply, body); err != nil {
		return err
	}
	if then != nil {
		then()
	}
	return nil
}
