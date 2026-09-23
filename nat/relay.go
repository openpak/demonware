package nat

import (
	"log/slog"
	"net"
	"sync"
	"time"
)

// Relay gives a joiner a relay address for a host it may not reach directly. The address is
// written into a free relay slot of the joiner's copy of the host's bdCommonAddr, one UDP
// port per (host, joiner) pair, and the port forwards between the host's public address and
// the joiner (docs/protocol.md §5). Off unless configured, and limited to the listed players.
type Relay struct {
	IP        net.IP
	Low, High int
	Allow     func(joiner uint64) bool
	Log       *slog.Logger

	mu    sync.Mutex
	pairs map[[2]uint64]*link
	next  int
}

type link struct {
	conn     *net.UDPConn
	port     int
	host     *net.UDPAddr
	joinerIP net.IP
	joiner   *net.UDPAddr
	last     time.Time
}

const commonAddrSize = 67

// Enabled reports whether the relay has ports.
func (r *Relay) Enabled() bool { return r != nil && r.IP.To4() != nil && r.Low > 0 && r.High >= r.Low }

// Offer returns hostAddr with a relay endpoint for this joiner in its first free relay slot,
// or hostAddr unchanged when the relay is off, not allowed for the joiner, or out of ports.
func (r *Relay) Offer(hostAddr []byte, host, joiner uint64, joinerIP net.IP) []byte {
	if !r.Enabled() || len(hostAddr) < commonAddrSize || host == 0 || joiner == 0 || host == joiner ||
		(r.Allow != nil && !r.Allow(joiner)) {
		return hostAddr
	}
	public := slotAddr(hostAddr[60:66])
	if public == nil {
		return hostAddr
	}
	free := -1
	for slot := 1; slot <= 9; slot++ {
		if slotAddr(hostAddr[slot*6:slot*6+6]) == nil {
			free = slot
			break
		}
	}
	if free < 0 {
		return hostAddr
	}
	p := r.open([2]uint64{host, joiner}, public, joinerIP)
	if p == nil {
		return hostAddr
	}
	out := append([]byte(nil), hostAddr...)
	copy(out[free*6:], addr(r.IP, p.port))
	return out
}

func (r *Relay) open(key [2]uint64, host *net.UDPAddr, joinerIP net.IP) *link {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pairs == nil {
		r.pairs = map[[2]uint64]*link{}
		r.next = r.Low
		go r.sweep()
	}
	if p := r.pairs[key]; p != nil {
		p.host, p.joinerIP, p.last = host, joinerIP, time.Now()
		return p
	}
	used := map[int]bool{}
	for _, p := range r.pairs {
		used[p.port] = true
	}
	for range r.High - r.Low + 1 {
		port := r.next
		if r.next++; r.next > r.High {
			r.next = r.Low
		}
		if used[port] {
			continue
		}
		c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
		if err != nil {
			continue
		}
		p := &link{conn: c, port: port, host: host, joinerIP: joinerIP, last: time.Now()}
		r.pairs[key] = p
		go r.forward(p)
		r.Log.Info("relay: pair opened", "host", key[0], "joiner", key[1], "port", port)
		return p
	}
	r.Log.Warn("relay: no free port", "host", key[0], "joiner", key[1])
	return nil
}

// forward moves packets between the host and the joiner. The joiner's port is learned from
// its first packet, which must come from the address it has on the lobby.
func (r *Relay) forward(p *link) {
	buf := make([]byte, 2048)
	for {
		n, from, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		r.mu.Lock()
		var to *net.UDPAddr
		switch {
		case from.IP.Equal(p.host.IP) && from.Port == p.host.Port:
			to = p.joiner
		case from.IP.Equal(p.joinerIP):
			p.joiner, to = from, p.host
		}
		if to != nil {
			p.last = time.Now()
		}
		r.mu.Unlock()
		if to != nil {
			_, _ = p.conn.WriteToUDP(buf[:n], to)
		}
	}
}

func (r *Relay) sweep() {
	for range time.Tick(30 * time.Second) {
		r.mu.Lock()
		for k, p := range r.pairs {
			if time.Since(p.last) > 2*time.Minute {
				p.conn.Close()
				delete(r.pairs, k)
			}
		}
		r.mu.Unlock()
	}
}
