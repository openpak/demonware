package auth

import (
	"bufio"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProxyListener reads a PROXY protocol v1 header ("PROXY TCP4 src dst sport dport\r\n") at
// the start of every connection and reports the source it names as the remote address. Use
// it only behind a proxy that always sends the header (Traefik with proxyProtocol version 1
// on the service): a connection without one is closed.
type ProxyListener struct{ net.Listener }

func (l ProxyListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &proxyConn{Conn: c, r: bufio.NewReaderSize(c, 256)}, nil
}

type proxyConn struct {
	net.Conn
	r    *bufio.Reader
	once sync.Once
	src  net.Addr
	err  error
}

// header is parsed on first use, in the connection's own goroutine, not in Accept.
func (c *proxyConn) header() error {
	c.once.Do(func() {
		_ = c.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := c.r.ReadString('\n')
		_ = c.Conn.SetReadDeadline(time.Time{})
		if err != nil || len(line) > 107 || !strings.HasSuffix(line, "\r\n") {
			c.err = errors.New("proxy protocol: no v1 header")
			return
		}
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "PROXY" {
			c.err = errors.New("proxy protocol: no v1 header")
			return
		}
		if f[1] == "UNKNOWN" {
			return
		}
		if len(f) != 6 || (f[1] != "TCP4" && f[1] != "TCP6") {
			c.err = errors.New("proxy protocol: malformed header")
			return
		}
		ip := net.ParseIP(f[2])
		port, perr := strconv.Atoi(f[4])
		if ip == nil || perr != nil || port < 0 || port > 65535 {
			c.err = errors.New("proxy protocol: malformed address")
			return
		}
		c.src = &net.TCPAddr{IP: ip, Port: port}
	})
	return c.err
}

func (c *proxyConn) Read(b []byte) (int, error) {
	if err := c.header(); err != nil {
		c.Conn.Close()
		return 0, err
	}
	return c.r.Read(b)
}

func (c *proxyConn) RemoteAddr() net.Addr {
	if c.header() == nil && c.src != nil {
		return c.src
	}
	return c.Conn.RemoteAddr()
}
