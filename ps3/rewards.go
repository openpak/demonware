package ps3

import (
	"context"
	"sync"
	"time"

	emit "github.com/openpak/rewards-emit"
)

// EmitRewards reports PS3 game sessions through rewards-emit. A host's open game is a room; a
// room counts only when the rewards session clock sees at least two mapped players in it for
// REWARDS_SESSION_MIN. The lobby cannot see who joins a game (the join is peer to peer), so for
// now every room has one player and nothing is emitted; Join is the hook for the day a
// captured task or an NP signal proves a second player (docs/ps3.md, "Rewards").
type EmitRewards struct {
	reg    emit.Registry
	mu     sync.Mutex
	hosted map[string][]string // online id -> room ids
}

// NewEmitRewards starts the session watcher on rw's clock until ctx ends. metric is the rewards
// metric of the game, e.g. "game_sessions.call-of-duty-modern-warfare-3".
func NewEmitRewards(ctx context.Context, rw *emit.Client, metric string) *EmitRewards {
	r := &EmitRewards{hosted: map[string][]string{}}
	s := emit.NewSessions(rw, metric, 0)
	rw.PersistSessions("demonware-ps3-"+metric, s)
	go s.Watch(ctx, 20*time.Second, r.Snapshot)
	return r
}

func id(onlineID string) emit.Identity { return emit.Identity{Namespace: "np", Subject: onlineID} }

func (r *EmitRewards) Hosted(title uint32, game []byte, onlineID string) {
	room := string(hexOf(game))
	r.mu.Lock()
	r.hosted[onlineID] = append(r.hosted[onlineID], room)
	r.mu.Unlock()
	r.reg.Join(room, id(onlineID))
}

// Join records a second player in a room (not called yet, see the type comment).
func (r *EmitRewards) Join(game []byte, onlineID string) {
	r.reg.Join(string(hexOf(game)), id(onlineID))
}

func (r *EmitRewards) Ended(game []byte, onlineID string) {
	r.reg.Leave(string(hexOf(game)), id(onlineID))
}

func (r *EmitRewards) Left(onlineID string) {
	r.mu.Lock()
	rooms := r.hosted[onlineID]
	delete(r.hosted, onlineID)
	r.mu.Unlock()
	for _, room := range rooms {
		r.reg.Leave(room, id(onlineID))
	}
}

// Snapshot is the rooms for emit.Sessions.Watch.
func (r *EmitRewards) Snapshot() []emit.Room { return r.reg.Snapshot() }

func hexOf(b []byte) []byte {
	const d = "0123456789abcdef"
	o := make([]byte, 0, len(b)*2)
	for _, c := range b {
		o = append(o, d[c>>4], d[c&15])
	}
	return o
}
