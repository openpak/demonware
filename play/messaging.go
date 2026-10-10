package play

import (
	"unicode/utf8"

	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/wire"
)

// Service 6 (messaging), docs/protocol.md §6.
const (
	SvcMessaging      = 6
	taskSendMessage   = 14
	pushGlobalMessage = 21
	maxMessage        = 4096 // the client sends and reads at most this many bytes
	maxSenderName     = 63   // the client reads the name into 64 bytes
)

// 6/14: a message to one player (u64 online id, blob). Diablo III's party invites, join
// requests and their answers all travel this way. The recipient, when connected to the same
// title, gets push 21 (sender online id, sender nickname, the blob as sent).
func (s *Shared) sendMessage(c *lobby.Conn, r *lobby.Request) lobby.Response {
	to, e1 := r.Args.U64()
	data, e2 := r.Args.Blob()
	if e1 != nil || e2 != nil || len(data) > maxMessage {
		c.Log().Info("message unreadable", "args", wire.Describe(r.Raw[min(3, len(r.Raw)):]))
		return reply(wire.Fail(wire.Failed))
	}
	var dest *lobby.Conn
	if to != c.OnlineID() {
		dest = s.Online(c.Title(), to)
	}
	c.Log().Info("message", "bytes", len(data), "delivered", dest != nil)
	if dest == nil {
		// Offline, in another title or the sender itself: nobody to deliver to.
		return reply(wire.Empty(r.Task))
	}
	push := messagePush(c.OnlineID(), c.Player().Nickname, data)
	return lobby.Response{Body: wire.Empty(r.Task), Then: func() {
		if err := dest.Push(push); err != nil {
			dest.Log().Info("message push failed")
		}
	}}
}

// messagePush is push 21 as the client reads it: event, sender online id, sender name, blob.
func messagePush(from uint64, name string, data []byte) []byte {
	return wire.Push(pushGlobalMessage, func(w *wire.Writer) {
		w.U64(from).String(senderName(name)).Blob(data)
	})
}

// senderName cuts a nickname to what the client's 64-byte field holds, on a rune boundary.
func senderName(n string) string {
	for len(n) > maxSenderName {
		_, size := utf8.DecodeLastRuneInString(n)
		n = n[:len(n)-size]
	}
	return n
}
