package ps3

import (
	"bufio"
	"net"
	"time"
)

// Split shares one listener between the Switch lobby and the PS3 server. It reads the first
// eight bytes of each connection:
//
//   - Switch hello: u32 200 | u32 200 | ...   (bytes 4 to 7 are c8 00 00 00)
//   - PS3 lobby:    c8 00 00 00 ff ff 00 00   (Modern Warfare 3; Modern Warfare 2 starts b4)
//   - PS3 auth:     u32 length | 00 | 12 | ...   (byte 4 is the encrypted flag, 0)
//
// so byte 4 equal to 0xC8 with bytes 5 to 7 zero is Switch and everything else is PS3. The
// returned listener yields only the Switch connections, with the peeked bytes put back; PS3
// connections go to handle. UDP needs no split: the NAT probes have the same shape.
func Split(ln net.Listener, handle func(net.Conn)) net.Listener {
	out := make(chan net.Conn)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				close(out)
				return
			}
			go route(c, handle, out)
		}
	}()
	return &splitListener{Listener: ln, ch: out}
}

type splitListener struct {
	net.Listener
	ch chan net.Conn
}

func (l *splitListener) Accept() (net.Conn, error) {
	c, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

type peeked struct {
	net.Conn
	r *bufio.Reader
}

func (p *peeked) Read(b []byte) (int, error) { return p.r.Read(b) }

// IsSwitch reports whether the first eight bytes are a Switch lobby hello.
func IsSwitch(head []byte) bool {
	return len(head) >= 8 && head[4] == 0xC8 && head[5] == 0 && head[6] == 0 && head[7] == 0
}

func route(c net.Conn, handle func(net.Conn), sw chan<- net.Conn) {
	br := bufio.NewReaderSize(c, 64<<10)
	_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
	head, err := br.Peek(8)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	pc := &peeked{Conn: c, r: br}
	if IsSwitch(head) {
		sw <- pc
		return
	}
	handle(pc)
}
